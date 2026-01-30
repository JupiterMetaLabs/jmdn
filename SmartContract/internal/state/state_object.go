package state

import (
	"bytes"
	"context"
	"math/big"

	"gossipnode/SmartContract/internal/storage"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	pbdid "gossipnode/DID/proto"
)

// stateObject represents a single Ethereum account.
// It tracks both the committed state (from DB) and the transient state (in-memory changes).
type stateObject struct {
	address  common.Address
	addrHash common.Hash // Keccak256 hash of address

	// Data corresponding to the state at the beginning of the transaction (from DB)
	originAccount *AccountData

	// Data corresponding to the current state in the transaction (Dirty)
	data *AccountData

	// Storage Caching
	// Origin: Value at start of Tx
	// Dirty: Value currently changed to
	originStorage map[common.Hash]common.Hash
	dirtyStorage  map[common.Hash]common.Hash

	// Code
	code []byte

	// Flags
	dirtyCode bool // Has code been updated?
	dirty     bool // Has any field (Balance/Nonce) changed?
	suicided  bool // Has SelfDestruct been called?
	deleted   bool // Should this object be removed from DB?

	// DB Reference (to lazy load storage/code if needed)
	db *ContractDB
}

// newStateObject creates a new state object with the given origin data.
func newStateObject(db *ContractDB, addr common.Address, origin *AccountData) *stateObject {
	if origin == nil {
		origin = NewAccountData()
	}

	return &stateObject{
		address:       addr,
		addrHash:      crypto.Keccak256Hash(addr[:]),
		originAccount: origin,
		data:          origin.Copy(),
		originStorage: make(map[common.Hash]common.Hash),
		dirtyStorage:  make(map[common.Hash]common.Hash),
		db:            db,
	}
}

// ============================================================================
// Balance Methods
// ============================================================================

// getBalance returns the current balance.
func (s *stateObject) getBalance() *uint256.Int {
	return new(uint256.Int).Set(s.data.Balance)
}

// setBalance updates the balance and marks the object as dirty.
func (s *stateObject) setBalance(amount *uint256.Int) {
	s.data.Balance = new(uint256.Int).Set(amount)
	s.markDirty()
}

// addBalance adds the given amount to the balance.
func (s *stateObject) addBalance(amount *uint256.Int) {
	s.setBalance(new(uint256.Int).Add(s.data.Balance, amount))
}

// subBalance subtracts the given amount from the balance.
func (s *stateObject) subBalance(amount *uint256.Int) {
	s.setBalance(new(uint256.Int).Sub(s.data.Balance, amount))
}

// ============================================================================
// Nonce Methods
// ============================================================================

// getNonce returns the current nonce.
func (s *stateObject) getNonce() uint64 {
	return s.data.Nonce
}

// setNonce updates the nonce and marks the object as dirty.
func (s *stateObject) setNonce(nonce uint64) {
	s.data.Nonce = nonce
	s.markDirty()
}

// ============================================================================
// Code Methods
// ============================================================================

// getCode returns the contract code, lazily loading from DB if necessary.
func (s *stateObject) getCode() []byte {
	if s.code != nil {
		return s.code
	}

	// Lazy load from DB
	if len(s.data.CodeHash) == 0 || bytes.Equal(s.data.CodeHash, emptyCodeHash) {
		return nil
	}

	// Load from PebbleDB
	key := makeCodeKey(s.address)
	if val, err := s.db.db.Get(key); err == nil && len(val) > 0 {
		s.code = val
		return s.code
	}

	return nil
}

// setCode updates the contract code and marks the object as dirty.
func (s *stateObject) setCode(code []byte) {
	s.code = code
	s.data.CodeHash = crypto.Keccak256(code)
	s.dirtyCode = true
	s.markDirty()
}

// getCodeHash returns the code hash.
func (s *stateObject) getCodeHash() []byte {
	if len(s.data.CodeHash) == 0 {
		return emptyCodeHash
	}
	return s.data.CodeHash
}

// getCodeSize returns the size of the contract code.
func (s *stateObject) getCodeSize() int {
	return len(s.getCode())
}

// ============================================================================
// Storage Methods
// ============================================================================

// getState returns the current value for a storage slot, lazily loading if necessary.
func (s *stateObject) getState(key common.Hash) common.Hash {
	// Check dirty storage first
	if value, ok := s.dirtyStorage[key]; ok {
		return value
	}

	// Check origin storage
	if value, ok := s.originStorage[key]; ok {
		return value
	}

	// Lazy load from DB
	value := s.loadStorage(key)
	s.originStorage[key] = value
	return value
}

// setState updates a storage slot and marks it as dirty.
func (s *stateObject) setState(key, value common.Hash) {
	s.dirtyStorage[key] = value
	s.markDirty()
}

// getCommittedState returns the committed (origin) value for a storage slot.
func (s *stateObject) getCommittedState(key common.Hash) common.Hash {
	// Check origin storage
	if value, ok := s.originStorage[key]; ok {
		return value
	}

	// Lazy load from DB
	value := s.loadStorage(key)
	s.originStorage[key] = value
	return value
}

// loadStorage loads a storage value from the database.
func (s *stateObject) loadStorage(key common.Hash) common.Hash {
	dbKey := makeStorageKey(s.address, key)
	val, err := s.db.db.Get(dbKey)
	if err != nil || len(val) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(val)
}

// ============================================================================
// State Management
// ============================================================================

// markDirty marks the object as having been modified.
func (s *stateObject) markDirty() {
	s.dirty = true
}

// isDirty returns whether the object has been modified.
func (s *stateObject) isDirty() bool {
	return s.dirty
}

// isEmpty returns true if the account is considered empty.
// An account is empty if it has zero nonce, zero balance, and no code.
func (s *stateObject) isEmpty() bool {
	return s.data.Nonce == 0 &&
		s.data.Balance.Sign() == 0 &&
		bytes.Equal(s.data.CodeHash, emptyCodeHash)
}

// suicide marks the account for deletion.
func (s *stateObject) suicide() {
	s.suicided = true
	s.deleted = true
}

// ============================================================================
// Persistence Support
// ============================================================================

// finalizeStorage prepares storage changes for persistence.
// Returns a map of storage keys to write and a list of keys to delete.
func (s *stateObject) finalizeStorage() (toWrite map[common.Hash]common.Hash, toDelete []common.Hash) {
	toWrite = make(map[common.Hash]common.Hash)
	toDelete = make([]common.Hash, 0)

	for key, value := range s.dirtyStorage {
		if value == (common.Hash{}) {
			// Empty value means delete
			toDelete = append(toDelete, key)
		} else {
			toWrite[key] = value
		}
	}

	return toWrite, toDelete
}

// commitStorage moves dirty storage to origin storage and clears dirty map.
func (s *stateObject) commitStorage() {
	for key, value := range s.dirtyStorage {
		s.originStorage[key] = value
	}
	s.dirtyStorage = make(map[common.Hash]common.Hash)
}

// commitCode clears the dirty code flag after persistence.
func (s *stateObject) commitCode() {
	s.dirtyCode = false
}

// commitState clears all dirty flags after successful commit.
func (s *stateObject) commitState() {
	s.commitStorage()
	s.commitCode()
	s.dirty = false
	s.originAccount = s.data.Copy()
}

// ============================================================================
// Helper Functions for ContractDB Integration
// ============================================================================

// loadAccountFromDID loads account data from the DID service.
// This is used for lazy loading when an account is first accessed.
func loadAccountFromDID(didClient pbdid.DIDServiceClient, addr common.Address) *AccountData {
	if didClient == nil {
		return NewAccountData()
	}

	req := &pbdid.GetDIDRequest{Did: addr.Hex()}
	resp, err := didClient.GetDID(context.Background(), req)

	account := NewAccountData()

	if err != nil || resp.DidInfo == nil {
		return account
	}

	// Parse balance
	if resp.DidInfo.Balance != "" {
		bigBal := new(big.Int)
		if val, ok := bigBal.SetString(resp.DidInfo.Balance, 0); ok {
			account.Balance = new(uint256.Int)
			account.Balance.SetFromBig(val)
		}
	}

	// Parse nonce
	if resp.DidInfo.Nonce != "" {
		nonceBig := new(big.Int)
		if val, ok := nonceBig.SetString(resp.DidInfo.Nonce, 0); ok {
			account.Nonce = val.Uint64()
		}
	}

	return account
}

// loadAccountFromLocalDB loads account data from the local PebbleDB.
// This checks for locally cached nonce values.
func loadAccountFromLocalDB(db storage.KVStore, addr common.Address) *AccountData {
	account := NewAccountData()

	// Load nonce from local DB if available
	nonceKey := makeNonceKey(addr)
	if val, err := db.Get(nonceKey); err == nil && len(val) > 0 {
		account.Nonce = new(big.Int).SetBytes(val).Uint64()
	}

	// Note: We don't load balance from local DB, as it's managed by DID service
	// Storage and code are loaded lazily on-demand in stateObject methods

	return account
}
