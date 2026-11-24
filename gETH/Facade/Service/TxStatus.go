package Service

import (
	"context"
	"encoding/hex"
	"math/big"
	"strings"
	"sync"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/txstatus"

	"github.com/ethereum/go-ethereum/common"
)

// This file wires transaction-status resolution into the RPC facade.
//
// Layering note: the resolver itself lives in gossipnode/txstatus and knows
// nothing about DB_OPs, the mempool protos, or this package. Everything
// jmdn-specific is here — the chain-store adapter, the conversion from a
// mempool transaction to the facade's Tx type, and the process-wide instance.

// ─────────────────────────────────────────────────────────────────────────────
// Process-wide resolver
// ─────────────────────────────────────────────────────────────────────────────
//
// A package-level instance with a setter, matching the existing pattern for the
// routing client (Block.SetRoutingClient). The facade's Service is constructed
// per server (main.go builds one for HTTP and one for WS), so hanging the
// resolver off ServiceImpl would mean either changing NewService's signature in
// several places or building two resolvers with two independent negative caches
// and breakers. One shared instance is both simpler and more correct: the
// guards are meant to bound total load, which they cannot do if duplicated.

var (
	resolverMu       sync.RWMutex
	txStatusResolver *txstatus.Resolver
	pendingTxEnabled bool
)

// SetTxStatusResolver installs the process-wide status resolver. Passing nil
// disables the feature, which is the default state.
func SetTxStatusResolver(r *txstatus.Resolver) {
	resolverMu.Lock()
	txStatusResolver = r
	resolverMu.Unlock()
}

// SetPendingTxByHashEnabled controls whether eth_getTransactionByHash may
// answer from the mempool. Separate from the resolver being installed, because
// serving pending transactions changes what existing clients see and is
// therefore a second opt-in.
func SetPendingTxByHashEnabled(enabled bool) {
	resolverMu.Lock()
	pendingTxEnabled = enabled
	resolverMu.Unlock()
}

func getTxStatusResolver() *txstatus.Resolver {
	resolverMu.RLock()
	defer resolverMu.RUnlock()
	return txStatusResolver
}

func pendingTxByHashEnabled() bool {
	resolverMu.RLock()
	defer resolverMu.RUnlock()
	return pendingTxEnabled && txStatusResolver != nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Chain-store adapter
// ─────────────────────────────────────────────────────────────────────────────

// ChainStoreAdapter answers "is this hash in a block?" from jmdn's own store.
type ChainStoreAdapter struct{}

// NewChainStoreAdapter returns a txstatus.ChainStore backed by DB_OPs.
func NewChainStoreAdapter() txstatus.ChainStore { return ChainStoreAdapter{} }

// IsMined implements txstatus.ChainStore.
//
// It asks for the BLOCK containing the transaction rather than the transaction
// row, because "mined" means "in a block" — a transaction row without a
// resolvable block is not something we should report as mined. This mirrors
// what TxByHash already does before it will return block fields.
//
// A "not found" error is translated to (false, nil); every other error is
// returned, because the resolver must be able to tell "definitely not in a
// block" from "the database could not answer". Guessing `false` on a database
// failure would let the resolver go on to report `queued` or `unknown` for a
// transaction that is actually mined.
func (ChainStoreAdapter) IsMined(ctx context.Context, hash string) (bool, error) {
	block, err := DB_OPs.GetTransactionBlock(ctx, nil, hash)
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, err
	}
	return block != nil, nil
}

// isNotFoundErr recognises the store's "absent" signals.
//
// String matching is unpleasant, but DB_OPs returns errors built with
// fmt.Errorf rather than sentinel values, so there is nothing to compare
// against. Kept deliberately narrow: matching too broadly here would turn a
// real database failure into a confident "not mined".
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no rows") ||
		strings.Contains(msg, "does not exist")
}

// ─────────────────────────────────────────────────────────────────────────────
// Service methods
// ─────────────────────────────────────────────────────────────────────────────

// TxStatus resolves the status of a transaction hash.
//
// Returns txstatus.ErrDisabled when the feature is not enabled, so a caller can
// distinguish a switched-off feature from a negative answer.
func (s *ServiceImpl) TxStatus(ctx context.Context, hash string) (*txstatus.Result, error) {
	r := getTxStatusResolver()
	if r == nil {
		return nil, txstatus.ErrDisabled
	}
	return r.Resolve(ctx, hash)
}

// PendingTxByHash returns a queued mempool transaction in the facade's Tx form,
// or nil when the hash is not currently queued.
//
// The returned Tx deliberately carries NO block fields. That is the standard
// Ethereum representation of a pending transaction — blockHash, blockNumber and
// transactionIndex serialise as null — and marshalTx already emits null for
// each when they are unset, so no change to the marshaller is needed.
//
// This never returns an error for a mempool problem: an unreachable mempool
// yields (nil, nil), and eth_getTransactionByHash then answers null rather than
// failing. A status query must not be able to break a standard RPC method.
func (s *ServiceImpl) PendingTxByHash(ctx context.Context, hash string) (*Types.Tx, error) {
	if !pendingTxByHashEnabled() {
		return nil, nil
	}

	res, err := s.TxStatus(ctx, hash)
	if err != nil || res == nil {
		return nil, nil
	}
	if res.Status != txstatus.StatusQueued || res.Tx == nil {
		return nil, nil
	}

	return pendingTxToFacadeTx(res.Tx, s.GetChainIDValue()), nil
}

// pendingTxToFacadeTx converts a mempool transaction body to the facade's Tx.
//
// Every numeric field arrives as a decimal or hex string from the mempool, so
// each is parsed defensively: a field that cannot be parsed is left zero rather
// than failing the conversion, because a partially-populated pending
// transaction is more useful to a wallet than an error.
//
// IMPORTANT — encryption boundary: the mempool stores from/to/value/nonce/gas/
// data encrypted and keeps only hash/type/timestamp/chain_id/v/r/s in the
// clear. If the mempool node does not decrypt on the lookup path, the address
// and value fields below arrive empty and this produces a skeleton transaction.
// That is why the hash is the only field treated as required.
func pendingTxToFacadeTx(p *txstatus.PendingTx, globalChainID *big.Int) *Types.Tx {
	if p == nil || p.Hash == "" {
		return nil
	}

	tx := &Types.Tx{
		Hash:                 decodeHexBytes(p.Hash),
		From:                 decodeHexBytes(p.From),
		To:                   decodeHexBytes(p.To),
		Input:                p.Data,
		Nonce:                p.Nonce,
		Value:                parseNumericBytes(p.Value),
		GasPrice:             parseNumericBytes(p.GasPrice),
		Type:                 p.Type,
		MaxFeePerGas:         parseNumericBytes(p.MaxFee),
		MaxPriorityFeePerGas: parseNumericBytes(p.MaxPriorityFee),
		ChainID:              parseNumericBytes(p.ChainID),
		R:                    parseNumericBytes(p.R),
		S:                    parseNumericBytes(p.S),
		// BlockNumber, BlockHash and TransactionIndex are intentionally left
		// unset: this transaction is not in a block, and marshalTx emits null
		// for each of them when unset. Populating any of them with a placeholder
		// would tell a wallet the transaction was mined.
	}

	if gl, ok := parseNumeric(p.GasLimit); ok {
		tx.Gas = gl.Uint64()
	}
	if v, ok := parseNumeric(p.V); ok {
		tx.V = uint32(v.Uint64())
	}
	if len(tx.ChainID) == 0 && globalChainID != nil {
		tx.ChainID = globalChainID.Bytes()
	}

	if len(p.AccessList) > 0 {
		al := make(config.AccessList, 0, len(p.AccessList))
		for _, t := range p.AccessList {
			entry := config.AccessTuple{Address: decodeAddress(t.Address)}
			for _, k := range t.StorageKeys {
				entry.StorageKeys = append(entry.StorageKeys, decodeHash(k))
			}
			al = append(al, entry)
		}
		tx.AccessList = &al
	}

	return tx
}

// decodeHexBytes decodes a 0x-prefixed (or bare) hex string. An unparseable
// value yields nil, which marshals as an empty field rather than an error.
func decodeHexBytes(s string) []byte {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return nil
	}
	if len(s)%2 == 1 {
		s = "0" + s
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// parseNumeric parses a mempool numeric field, accepting both 0x-prefixed hex
// and plain decimal — the mempool carries these as strings and the encoding is
// not guaranteed across transaction types.
func parseNumeric(s string) (*big.Int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, ok := new(big.Int).SetString(s[2:], 16)
		return n, ok
	}
	n, ok := new(big.Int).SetString(s, 10)
	return n, ok
}

// parseNumericBytes is parseNumeric as a big-endian byte slice, which is how
// Types.Tx carries numbers.
func parseNumericBytes(s string) []byte {
	n, ok := parseNumeric(s)
	if !ok || n == nil {
		return nil
	}
	return n.Bytes()
}

// decodeAddress right-aligns a hex string into a 20-byte address, matching how
// go-ethereum's SetBytes treats short and over-long inputs.
func decodeAddress(s string) common.Address {
	var a common.Address
	b := decodeHexBytes(s)
	if len(b) == 0 {
		return a
	}
	if len(b) > len(a) {
		b = b[len(b)-len(a):]
	}
	copy(a[len(a)-len(b):], b)
	return a
}

// decodeHash right-aligns a hex string into a 32-byte hash.
func decodeHash(s string) common.Hash {
	var h common.Hash
	b := decodeHexBytes(s)
	if len(b) == 0 {
		return h
	}
	if len(b) > len(h) {
		b = b[len(b)-len(h):]
	}
	copy(h[len(h)-len(b):], b)
	return h
}
