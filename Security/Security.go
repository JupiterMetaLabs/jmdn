package txverifier

import (
    "github.com/ethereum/go-ethereum/common"
    "github.com/ethereum/go-ethereum/core/types"
    "math/big"
)

// Transaction is your struct; make sure AccessList here is the same type as types.AccessList
type Transaction struct {
    From                 *common.Address
    ChainID              *big.Int
    Nonce                uint64
    To                   *common.Address
    Value                *big.Int
    Data                 []byte
    GasLimit             uint64
    GasPrice             *big.Int
    MaxPriorityFeePerGas *big.Int
    MaxFeePerGas         *big.Int
    AccessList           types.AccessList
    V, R, S              *big.Int
}

// VerifySignature checks that tx.V/R/S is a valid ECDSA signature over the
// EIP-155/EIP-2930/EIP-1559 signing hash of the transaction fields, and that
// the recovered address equals tx.From.
func VerifySignature(tx *Transaction) (bool, error) {
    // 1) Reconstruct the go-ethereum Transaction of the correct type
    var ethTx *types.Transaction

    switch {
    case tx.MaxFeePerGas != nil && tx.MaxPriorityFeePerGas != nil:
        // EIP-1559 (Type 2)
        inner := &types.DynamicFeeTx{
            ChainID:   tx.ChainID,
            Nonce:     tx.Nonce,
            To:        tx.To,
            Value:     tx.Value,
            GasTipCap: tx.MaxPriorityFeePerGas,
            GasFeeCap: tx.MaxFeePerGas,
            Gas:       tx.GasLimit,
            Data:      tx.Data,
            AccessList: tx.AccessList,
        }
        ethTx = types.NewTx(inner)

    case len(tx.AccessList) > 0:
        // EIP-2930 (Type 1)
        inner := &types.AccessListTx{
            ChainID:    tx.ChainID,
            Nonce:      tx.Nonce,
            To:         tx.To,
            Value:      tx.Value,
            GasPrice:   tx.GasPrice,
            Gas:        tx.GasLimit,
            Data:       tx.Data,
            AccessList: tx.AccessList,
        }
        ethTx = types.NewTx(inner)

    default:
        // Legacy (Type 0)
        inner := &types.LegacyTx{
            Nonce:    tx.Nonce,
            To:       tx.To,
            Value:    tx.Value,
            GasPrice: tx.GasPrice,
            Gas:      tx.GasLimit,
            Data:     tx.Data,
        }
        ethTx = types.NewTx(inner)
    }

    // 2) Use the “London” signer which knows how to hash & recover all three types
    signer := types.NewLondonSigner(tx.ChainID)

    // 3) Recover the sender address from V/R/S
    from, err := types.Sender(signer, ethTx)
    if err != nil {
        return false, err
    }

    // 4) Compare recovered address to tx.From
    return from == *tx.From, nil
}
