package transaction

import (
	"math/big"
	"time"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
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
