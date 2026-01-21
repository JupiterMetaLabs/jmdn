package state

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/rs/zerolog/log"
)

// CreateAccount creates a new account
func (s *ImmuStateDB) CreateAccount(addr common.Address) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Delete any existing account
	if obj := s.getStateObject(addr); obj != nil {
		// Mark as deleted if it existed
		s.suicided[addr] = true
	}

	// Create a new account
	newAccount := &stateAccount{
		Balance:      new(big.Int),
		BalanceU256:  uint256.NewInt(0),
		Nonce:        0,
		Code:         nil,
		CodeHash:     crypto.Keccak256Hash(nil),
		Storage:      make(map[common.Hash]common.Hash),
		StorageDirty: make(map[common.Hash]struct{}),
	}

	s.accounts[addr] = newAccount

	// Create object (this will use s.accounts[addr] we just set?)
	// Actually getOrCreateStateObject checks stateObjects first.
	// We should manually put it in stateObjects to avoid confusion or use internal helper correctly
	obj := &stateObject{
		address: addr,
		account: *newAccount, // This copies the struct? stateAccount contains pointers/maps so it's shallow copy
		isDirty: true,
		deleted: false,
	}
	s.stateObjects[addr] = obj

	s.stateObjectsDirty[addr] = struct{}{}
	delete(s.suicided, addr)
}

// GetBalance retrieves the balance of the given address
func (s *ImmuStateDB) GetBalance(addr common.Address) *uint256.Int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	stateObject := s.getOrCreateStateObject(addr)
	return stateObject.account.BalanceU256
}

// AddBalance adds amount to the account associated with addr
func (s *ImmuStateDB) AddBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	stateObject := s.getOrCreateStateObject(addr)

	// Add the amount
	newBalance, overflow := uint256.NewInt(0).AddOverflow(stateObject.account.BalanceU256, amount)
	if overflow {
		log.Warn().
			Str("address", addr.Hex()).
			Str("current", stateObject.account.BalanceU256.Hex()).
			Str("amount", amount.Hex()).
			Msg("Balance overflow detected, capping at maximum value")
		newBalance = uint256.NewInt(0).SetAllOne() // set to max value
	}

	stateObject.account.BalanceU256 = newBalance
	stateObject.account.Balance = newBalance.ToBig()
	stateObject.isDirty = true
	s.stateObjectsDirty[addr] = struct{}{}
}

// SubBalance subtracts amount from the account associated with addr
func (s *ImmuStateDB) SubBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	stateObject := s.getOrCreateStateObject(addr)

	// Check for underflow
	if stateObject.account.BalanceU256.Lt(amount) {
		log.Warn().
			Str("address", addr.Hex()).
			Str("current", stateObject.account.BalanceU256.Hex()).
			Str("amount", amount.Hex()).
			Msg("Balance underflow detected, setting to zero")
		stateObject.account.BalanceU256 = uint256.NewInt(0)
	} else {
		stateObject.account.BalanceU256.Sub(stateObject.account.BalanceU256, amount)
	}

	stateObject.account.Balance = stateObject.account.BalanceU256.ToBig()
	stateObject.isDirty = true
	s.stateObjectsDirty[addr] = struct{}{}
}

// GetNonce returns the nonce of the account
func (s *ImmuStateDB) GetNonce(addr common.Address) uint64 {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	stateObject := s.getOrCreateStateObject(addr)
	return stateObject.account.Nonce
}

// SetNonce sets the nonce of the account
func (s *ImmuStateDB) SetNonce(addr common.Address, nonce uint64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	stateObject := s.getOrCreateStateObject(addr)
	stateObject.account.Nonce = nonce
	stateObject.isDirty = true
	s.stateObjectsDirty[addr] = struct{}{}
}
