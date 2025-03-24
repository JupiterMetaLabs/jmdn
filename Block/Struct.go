package Block

import (
    "math/big"

    "github.com/ethereum/go-ethereum/common"
)

// AccessTuple is the element type of an access list.
type AccessTuple struct {
    Address     common.Address
    StorageKeys []common.Hash
}

// AccessList is an EIP-2930 access list.
type AccessList []AccessTuple

type Transaction struct {
    ChainID             *big.Int
    Nonce               uint64
    To                  *common.Address // nil for contract creation
    Value               *big.Int
    Data                []byte
    GasLimit            uint64
    GasPrice            *big.Int   // Only for Type 0 and Type 1
    MaxPriorityFeePerGas *big.Int  // Only for Type 2
    MaxFeePerGas         *big.Int  // Only for Type 2
    AccessList          AccessList // Now uses the locally defined type     // For EIP-2930 (Type 1) and EIP-1559 (Type 2)
    V, R, S             *big.Int   // Signature values
}