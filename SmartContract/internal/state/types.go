package state

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// stateAccount represents an Ethereum account
type stateAccount struct {
	Balance      *big.Int
	BalanceU256  *uint256.Int // Cached uint256 representation
	Nonce        uint64
	Code         []byte
	CodeHash     common.Hash
	Storage      map[common.Hash]common.Hash
	StorageDirty map[common.Hash]struct{}
}

// stateObject represents an Ethereum account with processing state
type stateObject struct {
	address common.Address
	account stateAccount
	isDirty bool
	deleted bool
}

// stateSnapshot represents a point-in-time snapshot of the state
type stateSnapshot struct {
	id       int
	accounts map[common.Address]stateAccount
	suicided map[common.Address]bool
}

// dbOperation represents a pending database operation
type dbOperation struct {
	key      string
	value    []byte
	isDelete bool
}

// accessList tracks access list for EIP-2930
type accessList struct {
	addresses map[common.Address]struct{}
	slots     map[common.Address]map[common.Hash]struct{}
}

// CodeIterator implements an iterator over all contracts
type CodeIterator struct {
	stateDB *ImmuStateDB
	addrs   []common.Address
	index   int
}
