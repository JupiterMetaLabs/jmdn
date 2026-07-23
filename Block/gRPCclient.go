// Package Block — MRE transport helpers.
//
// This file holds the domain→proto conversion (config.Transaction →
// commonv1.Transaction) and the thin submit facade used by the block server.
// The gRPC client itself lives in Singleton_RoutingClient.go behind the
// MempoolRouter port (routing_port.go).
//
// History: this file previously carried a second client (MempoolClient over
// the legacy MempoolService) plus several wrappers with no callers
// (GetTransaction, GetPendingTransactions, SubmitTransactions,
// WrapperGetFeeStatistics, per-client GetFeeStatistics/GetMempoolStats).
// All were deleted in the MRE v1 migration (tracker O-1): jmdn talks to the
// MRE only, through the port.
package Block

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"gossipnode/config"
	commonv1 "gossipnode/proto/v1/common"

	"github.com/ethereum/go-ethereum/common"
)

const (
	FILENAME   = ""
	TOPIC      = "mempool"
	BLOCKTOPIC = "block"
	KEEP_LOGS  = true
	TIMEOUT    = 5 * time.Second
)

// SubmitToMempool routes a signed transaction to the MRE via the singleton
// router. Kept as a package-level facade because the block server calls it
// from an async context (Server.go). Transport failure and MRE rejection both
// surface as an error here — the caller only logs.
func SubmitToMempool(loggerCtx context.Context, tx *config.Transaction, txHash string) error {
	router, err := GetRoutingClient(loggerCtx)
	if err != nil {
		return fmt.Errorf("routing client connection failed: %w", err)
	}

	result, err := router.SubmitTransaction(loggerCtx, tx, txHash)
	if err != nil {
		return err
	}
	if !result.Accepted {
		return fmt.Errorf("mempool rejected transaction: %s", result.RejectReason)
	}

	// Feed the pending-nonce tracker: this node has routed the tx, so its
	// nonce is committed-or-in-flight from this node's perspective even once
	// the sequencer pulls it out of the observable mempool (tracker D-5).
	if tx.From != nil {
		pendingNonceTracker.Record(tx.From.Hex(), tx.Nonce)
	}
	return nil
}

// GetFeeStatisticsFromRouting returns the MRE fee view via the port.
// Retained name for existing callers (RPC facade GasPrice).
func GetFeeStatisticsFromRouting(loggerCtx context.Context) (*FeeStats, error) {
	router, err := GetRoutingClient(loggerCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get routing client: %w", err)
	}
	return router.GetFeeStatistics(loggerCtx)
}

// convertToPbTransaction maps jmdn's internal transaction to the v1 wire type.
// The v1 commonv1.Transaction is field-identical to the legacy message
// (verified field-by-field; see docs/MRE-V1-MIGRATION-TRACKER.md §2).
func convertToPbTransaction(tx *config.Transaction, txHash string) *commonv1.Transaction {
	// big.Int → decimal string; nil-safe.
	getBigIntString := func(b *big.Int) string {
		if b == nil {
			return "0"
		}
		return b.String()
	}

	// Signature fields: nil/zero → "0x0", else 0x-prefixed hex.
	getSignatureString := func(b *big.Int) string {
		if b == nil || b.Cmp(big.NewInt(0)) == 0 {
			return "0x0"
		}
		return "0x" + b.Text(16)
	}

	getDataBytes := func(data []byte) []byte {
		if data == nil {
			return []byte{}
		}
		return data
	}

	addrToString := func(addr *common.Address) string {
		if addr == nil {
			return ""
		}
		return addr.Hex()
	}

	pbTx := &commonv1.Transaction{
		Hash:           txHash,
		From:           addrToString(tx.From),
		To:             addrToString(tx.To),
		Value:          getBigIntString(tx.Value),
		Type:           uint32(tx.Type), // 0=Legacy, 1=AccessList, 2=EIP-1559
		Timestamp:      uint64(tx.Timestamp),
		ChainId:        getBigIntString(tx.ChainID),
		Nonce:          uint64(tx.Nonce),
		GasLimit:       fmt.Sprintf("%d", tx.GasLimit),
		GasPrice:       getBigIntString(tx.GasPrice),
		MaxFee:         getBigIntString(tx.MaxFee),
		MaxPriorityFee: getBigIntString(tx.MaxPriorityFee),
		Data:           getDataBytes(tx.Data),
		AccessList:     convertAccessListToPb(tx.AccessList),
		V:              getSignatureString(tx.V),
		R:              getSignatureString(tx.R),
		S:              getSignatureString(tx.S),
	}

	// Legacy transactions: fall back to GasPrice as MaxFee if MaxFee unset,
	// so downstream fee handling always has an effective ceiling.
	if tx.Type == 0 && pbTx.MaxFee == "0" && tx.GasPrice != nil {
		pbTx.MaxFee = tx.GasPrice.String()
	}

	return pbTx
}

// convertAccessListToPb maps the EIP-2930 access list to the wire type.
func convertAccessListToPb(accessList config.AccessList) []*commonv1.AccessTuple {
	if len(accessList) == 0 {
		return nil
	}

	pbAccessList := make([]*commonv1.AccessTuple, len(accessList))
	for i, access := range accessList {
		storageKeys := make([]string, len(access.StorageKeys))
		for j, key := range access.StorageKeys {
			storageKeys[j] = key.Hex()
		}
		pbAccessList[i] = &commonv1.AccessTuple{
			Address:     access.Address.Hex(),
			StorageKeys: storageKeys,
		}
	}
	return pbAccessList
}
