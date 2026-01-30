package state

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// AccountData holds the pure data fields of an account.
// This represents the fundamental state of an Ethereum account.
type AccountData struct {
	Nonce    uint64       // Transaction count for this account
	Balance  *uint256.Int // Account balance in wei
	Root     common.Hash  // Storage root (Merkle root of contract storage)
	CodeHash []byte       // Hash of the contract bytecode
}

// NewAccountData creates a new empty AccountData instance.
func NewAccountData() *AccountData {
	return &AccountData{
		Nonce:    0,
		Balance:  uint256.NewInt(0),
		Root:     common.Hash{},
		CodeHash: emptyCodeHash,
	}
}

// Copy creates a deep copy of the AccountData.
// This is essential for maintaining separate origin and dirty states.
func (a *AccountData) Copy() *AccountData {
	if a == nil {
		return NewAccountData()
	}
	return &AccountData{
		Nonce:    a.Nonce,
		Balance:  new(uint256.Int).Set(a.Balance),
		Root:     a.Root,
		CodeHash: append([]byte(nil), a.CodeHash...),
	}
}

// Empty returns true if the account is considered empty.
// An account is empty if it has zero nonce, zero balance, and no code.
func (a *AccountData) Empty() bool {
	return a.Nonce == 0 &&
		a.Balance.Sign() == 0 &&
		len(a.CodeHash) == 0
}

// emptyCodeHash is the Keccak256 hash of empty code.
var emptyCodeHash = []byte{
	0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c,
	0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
	0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b,
	0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
}
