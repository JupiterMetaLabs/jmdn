package transaction

import (
	"fmt"
	"math/big"
	"time"

	"gossipnode/config"

	"crypto/ecdsa"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	gethTypes "github.com/ethereum/go-ethereum/core/types"
)

// BuildContractCreationTx constructs a new contract deployment transaction (EIP-1559).
// It returns the internal config.Transaction and its hash.
func BuildContractCreationTx(
	chainID *big.Int,
	sender common.Address,
	nonce uint64,
	value *big.Int,
	bytecode []byte,
	gasLimit uint64,
	maxFee *big.Int,
	maxPriorityFee *big.Int,
) (*config.Transaction, string, error) {

	// Create Type 2 (EIP-1559) transaction for contract deployment
	// To = nil indicates this is a contract creation transaction
	tx := &config.Transaction{
		Type:           2, // EIP-1559
		From:           &sender,
		To:             nil, // nil = contract creation
		Value:          value,
		Data:           bytecode,
		GasLimit:       gasLimit,
		Nonce:          nonce,
		ChainID:        chainID,
		MaxFee:         maxFee,
		MaxPriorityFee: maxPriorityFee,
		Timestamp:      uint64(time.Now().Unix()),
		// V, R, S will be populated by signature or remain empty if unsigned
	}

	// Calculate transaction hash compatible with Geth/Security module
	// We build a geth transaction purely to calculate the hash
	ethTxInner := &gethTypes.DynamicFeeTx{
		ChainID:    tx.ChainID,
		Nonce:      tx.Nonce,
		To:         tx.To,
		Value:      tx.Value,
		GasTipCap:  tx.MaxPriorityFee,
		GasFeeCap:  tx.MaxFee,
		Gas:        tx.GasLimit,
		Data:       tx.Data,
		AccessList: gethTypes.AccessList{}, // Empty for now
	}
	ethTx := gethTypes.NewTx(ethTxInner)
	txHash := ethTx.Hash().Hex()
	tx.Hash = common.HexToHash(txHash)

	return tx, txHash, nil
}

// SignTransaction signs the transaction with the provided private key using EIP-1559 signer.
// It populates the V, R, S fields of the config.Transaction.
func SignTransaction(tx *config.Transaction, privateKey *ecdsa.PrivateKey) error {
	if tx.ChainID == nil {
		return fmt.Errorf("transaction chain ID is missing")
	}

	// Reconstruct the geth transaction to sign it
	// Note: We need to reconstruct it effectively to use Geth's signing logic
	var ethTxInner gethTypes.TxData
	if tx.Type == 2 {
		ethTxInner = &gethTypes.DynamicFeeTx{
			ChainID:    tx.ChainID,
			Nonce:      tx.Nonce,
			To:         tx.To,
			Value:      tx.Value,
			GasTipCap:  tx.MaxPriorityFee,
			GasFeeCap:  tx.MaxFee,
			Gas:        tx.GasLimit,
			Data:       tx.Data,
			AccessList: gethTypes.AccessList{},
		}
	} else {
		// Fallback to legacy if needed, or error out
		return fmt.Errorf("only EIP-1559 (Type 2) transactions are currently supported for signing")
	}

	ethTx := gethTypes.NewTx(ethTxInner)
	signer := types.NewLondonSigner(tx.ChainID)

	signedTx, err := types.SignTx(ethTx, signer, privateKey)
	if err != nil {
		return fmt.Errorf("failed to sign transaction: %w", err)
	}

	v, r, s := signedTx.RawSignatureValues()
	tx.V = v
	tx.R = r
	tx.S = s

	return nil
}
