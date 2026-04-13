package state

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	pbdid "gossipnode/DID/proto"
	"gossipnode/SmartContract/internal/repository"
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
	originStorage    map[common.Hash]common.Hash
	dirtyStorage     map[common.Hash]common.Hash
	dirtyStorageMeta map[common.Hash]StorageMetadata // Metadata for changed slots

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
		address:          addr,
		addrHash:         crypto.Keccak256Hash(addr[:]),
		originAccount:    origin,
		data:             origin.Copy(),
		originStorage:    make(map[common.Hash]common.Hash),
		dirtyStorage:     make(map[common.Hash]common.Hash),
		dirtyStorageMeta: make(map[common.Hash]StorageMetadata),
		db:               db,
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

	// Load from repository because CodeHash might not be initialized
	// when the account data is first loaded from the generalized DID service
	val, err := s.db.repo.GetCode(context.Background(), s.address)
	fmt.Printf("DEBUG(state_object): repo GetCode -> len=%d, err=%v\n", len(val), err)
	if err == nil && len(val) > 0 {
		s.code = val
		s.data.CodeHash = crypto.Keccak256(val)
		return s.code
	}

	return nil
}

// setCode updates the contract code and marks the object as dirty.
func (s *stateObject) setCode(code []byte) {
	fmt.Printf("DEBUG(state_object): setCode called for %s with len %d\n", s.address.Hex(), len(code))
	s.code = code
	s.data.CodeHash = crypto.Keccak256(code)
	s.dirtyCode = true
	s.markDirty()
}

// getCodeHash returns the code hash.
func (s *stateObject) getCodeHash() []byte {
	if len(s.data.CodeHash) == 0 || bytes.Equal(s.data.CodeHash, emptyCodeHash) {
		// Attempt to load code to hydrate CodeHash correctly
		if code := s.getCode(); code == nil {
			return emptyCodeHash
		}
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

	// Track metadata
	// Note: We access db fields directly here assuming lock is held by caller (EVM usually is single-threaded per tx)
	// or relies on StateDB lock. However, stateObject methods are generally called under StateDB lock.
	s.dirtyStorageMeta[key] = StorageMetadata{
		ContractAddress:   s.address,
		StorageKey:        key,
		ValueHash:         crypto.Keccak256Hash(value.Bytes()),
		LastModifiedBlock: s.db.currentBlock,
		LastModifiedTx:    s.db.currentTxHash,
		UpdatedAt:         time.Now().UTC().Unix(),
	}

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
	val, err := s.db.repo.GetStorage(context.Background(), s.address, key)
	if err != nil {
		return common.Hash{}
	}
	return val
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
		bytes.Equal(s.getCodeHash(), emptyCodeHash)
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
// Returns map of storage keys to write, list of keys to delete, and map of metadata updates.
func (s *stateObject) finalizeStorage() (toWrite map[common.Hash]common.Hash, toDelete []common.Hash, metaUpdates map[common.Hash]StorageMetadata) {
	toWrite = make(map[common.Hash]common.Hash)
	toDelete = make([]common.Hash, 0)
	metaUpdates = make(map[common.Hash]StorageMetadata)

	for key, value := range s.dirtyStorage {
		if value == (common.Hash{}) {
			// Empty value means delete
			toDelete = append(toDelete, key)
		} else {
			toWrite[key] = value
			if meta, ok := s.dirtyStorageMeta[key]; ok {
				metaUpdates[key] = meta
			}
		}
	}

	return toWrite, toDelete, metaUpdates
}

// commitStorage moves dirty storage to origin storage and clears dirty map.
func (s *stateObject) commitStorage() {
	for key, value := range s.dirtyStorage {
		s.originStorage[key] = value
	}
	s.dirtyStorage = make(map[common.Hash]common.Hash)
	s.dirtyStorageMeta = make(map[common.Hash]StorageMetadata)
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

// loadAccountFromLocalDB loads account data from the local repository.
// This checks for locally cached nonce values.
func loadAccountFromLocalDB(repo repository.StateRepository, addr common.Address) *AccountData {
	account := NewAccountData()

	// Load nonce from local DB if available
	if nonce, err := repo.GetNonce(context.Background(), addr); err == nil && nonce > 0 {
		account.Nonce = nonce
	}

	// Note: We don't load balance from local DB, as it's managed by DID service
	// Storage and code are loaded lazily on-demand in stateObject methods

	return account
}
