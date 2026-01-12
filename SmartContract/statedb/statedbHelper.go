package statedb

import (
	"bytes"
	"fmt"
	"gossipnode/DB_OPs"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie/utils"
	"github.com/holiman/uint256"
	"github.com/rs/zerolog/log"
)

// Add these methods to the ImmuStateDB struct
// AccessList tracking for EIP-2930
type accessList struct {
	addresses map[common.Address]struct{}
	slots     map[common.Address]map[common.Hash]struct{}
}

// AddAddressToAccessList adds an address to the access list
func (s *ImmuStateDB) AddAddressToAccessList(addr common.Address) {
	// Initialize the access list if it doesn't exist
	if s.accessList == nil {
		s.accessList = &accessList{
			addresses: make(map[common.Address]struct{}),
			slots:     make(map[common.Address]map[common.Hash]struct{}),
		}
	}
	s.accessList.addresses[addr] = struct{}{}
}

// AddSlotToAccessList adds the specified contract slot to the access list
func (s *ImmuStateDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	// Initialize the access list if it doesn't exist
	if s.accessList == nil {
		s.accessList = &accessList{
			addresses: make(map[common.Address]struct{}),
			slots:     make(map[common.Address]map[common.Hash]struct{}),
		}
	}

	// Initialize the address's slot map if needed
	if _, ok := s.accessList.slots[addr]; !ok {
		s.accessList.slots[addr] = make(map[common.Hash]struct{})
	}

	s.accessList.slots[addr][slot] = struct{}{}
	s.accessList.addresses[addr] = struct{}{}
}

// AddressInAccessList returns whether an address is in the access list
func (s *ImmuStateDB) AddressInAccessList(addr common.Address) bool {
	if s.accessList == nil {
		return false
	}
	_, ok := s.accessList.addresses[addr]
	return ok
}

// SlotInAccessList returns whether the specified slot of an address is in the access list
func (s *ImmuStateDB) SlotInAccessList(addr common.Address, slot common.Hash) (bool, bool) {
	// First bool: is the slot in the access list
	// Second bool: is the address in the access list

	addrPresent := false
	if _, ok := s.accessList.addresses[addr]; ok {
		addrPresent = true
	}

	slotPresent := false
	if slots, ok := s.accessList.slots[addr]; ok {
		if _, ok := slots[slot]; ok {
			slotPresent = true
		}
	}

	return slotPresent, addrPresent
}

// AddPreimage adds a preimage of a hash to the database
func (s *ImmuStateDB) AddPreimage(hash common.Hash, preimage []byte) {
	// This is an optimization for debugging/tracing
	// For a minimal implementation, this can be a no-op
	// If you want to store preimages, you would need to add:
	//
	preimageKey := fmt.Sprintf("preimage:%s", hash.Hex())
	s.queueDBOperation(preimageKey, preimage, false)
}

// Preimages returns a map of hash -> preimage
func (s *ImmuStateDB) Preimages() map[common.Hash][]byte {
	// Return an empty map for a minimal implementation
	return make(map[common.Hash][]byte)
}

// GetPreimage returns the preimage for a hash if it's known
func (s *ImmuStateDB) GetPreimage(hash common.Hash) []byte {
	// Return nil for a minimal implementation
	return nil
}

// CreateContract creates a new contract and returns its address
func (s *ImmuStateDB) CreateContract(creator common.Address) {
	// Get and increment nonce
	nonce := s.GetNonce(creator)
	s.SetNonce(creator, nonce+1)

	// Generate contract address
	addr := crypto.CreateAddress(creator, nonce)

	// Create account using the method already defined in statedb.go
	s.CreateAccount(addr) // Assuming CreateAccount is defined in statedb.go
}

// Add this method to your ImmuStateDB struct
func (s *ImmuStateDB) GetStorageRoot(addr common.Address) common.Hash {
	// In a full implementation, this would return the Merkle root of the account's storage
	// For a simplified implementation, we'll hash the account's storage or return empty
	obj := s.getStateObject(addr)
	if obj == nil || len(obj.account.Storage) == 0 {
		return common.Hash{} // Return empty hash for non-existent or empty accounts
	}

	// Create a simple hash of the storage (this is not a true Merkle root!)
	h := crypto.NewKeccakState()

	// Add all storage slots to the hash in a deterministic order
	keys := make([]common.Hash, 0, len(obj.account.Storage))
	for key := range obj.account.Storage {
		keys = append(keys, key)
	}

	// Sort keys for deterministic output
	// Note: this can be removed if you don't need true determinism
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i][:], keys[j][:]) < 0
	})

	for _, key := range keys {
		value := obj.account.Storage[key]
		h.Write(key[:])
		h.Write(value[:])
	}

	var storageRoot common.Hash
	h.Read(storageRoot[:])
	return storageRoot
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

func (s *ImmuStateDB) Commit() (common.Hash, error) {
	return s.CommitToDB(true)
}

// Nonce Methods
// ============

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

// Code Methods
// ===========

// GetCode returns the contract code associated with this account
func (s *ImmuStateDB) GetCode(addr common.Address) []byte {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	addrBytes := addr.Bytes()

	if s.witness != nil {
		s.witnessMutex.Lock()
		s.witness.AddCode(addrBytes)
		s.witnessMutex.Unlock()
	}
	stateObject := s.getStateObject(addr)
	if stateObject == nil {
		return nil
	}

	if len(stateObject.account.Code) > 0 {
		return stateObject.account.Code
	}

	// Try loading code from database if not in memory
	codeKey := getDBKey(prefixCode, addr)
	code, err := DB_OPs.Read(s.dbClient, codeKey)
	if err != nil {
		return nil
	}

	// Update in-memory state
	stateObject.account.Code = code
	return code
}

// GetCodeSize returns the size of the contract code
func (s *ImmuStateDB) GetCodeSize(addr common.Address) int {
	code := s.GetCode(addr)
	return len(code)
}

// GetCodeHash returns the code hash of the given account
func (s *ImmuStateDB) GetCodeHash(addr common.Address) common.Hash {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	stateObject := s.getStateObject(addr)
	if stateObject == nil {
		return common.Hash{}
	}
	return stateObject.account.CodeHash
}

// SetCode sets the contract code
func (s *ImmuStateDB) SetCode(addr common.Address, code []byte) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	stateObject := s.getOrCreateStateObject(addr)
	stateObject.account.Code = code
	stateObject.account.CodeHash = crypto.Keccak256Hash(code)
	stateObject.isDirty = true
	s.stateObjectsDirty[addr] = struct{}{}
}

// Storage Methods
// ==============

// GetState returns the value at key in the storage of the given account
func (s *ImmuStateDB) GetState(addr common.Address, key common.Hash) common.Hash {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	stateObject := s.getStateObject(addr)
	if stateObject == nil {
		return common.Hash{}
	}

	// Check if the value is in memory
	if value, exists := stateObject.account.Storage[key]; exists {
		return value
	}

	// Not in memory, try to load from the database
	value, err := s.loadStorage(addr, key)
	if err != nil {
		return common.Hash{}
	}

	return value
}

// SetState sets the storage key-value pair
func (s *ImmuStateDB) SetState(addr common.Address, key, value common.Hash) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	stateObject := s.getOrCreateStateObject(addr)
	stateObject.account.Storage[key] = value
	stateObject.account.StorageDirty[key] = struct{}{}
	stateObject.isDirty = true
	s.stateObjectsDirty[addr] = struct{}{}
}

// Suicide Methods
// ==============

// Suicide marks the given account as suicided
func (s *ImmuStateDB) Suicide(addr common.Address) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	stateObject := s.getStateObject(addr)
	if stateObject == nil {
		return false
	}

	s.suicided[addr] = true
	stateObject.deleted = true

	return true
}

// HasSuicided returns true if the account was suicided
func (s *ImmuStateDB) HasSuicided(addr common.Address) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.suicided[addr]
}

// Refund Methods
// =============

// AddRefund adds gas to the refund counter
func (s *ImmuStateDB) AddRefund(gas uint64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.refund += gas
}

// SubRefund removes gas from the refund counter
func (s *ImmuStateDB) SubRefund(gas uint64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if gas > s.refund {
		s.refund = 0
	} else {
		s.refund -= gas
	}
}

// GetRefund returns the current value of the refund counter
func (s *ImmuStateDB) GetRefund() uint64 {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.refund
}

// Snapshot Methods
// ===============

// Snapshot returns an identifier for the current state point
func (s *ImmuStateDB) Snapshot() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	id := len(s.snapshots)

	// Create a deep copy of the current state
	accountsCopy := make(map[common.Address]stateAccount)
	for addr, account := range s.accounts {
		// Deep copy of the account
		storageCopy := make(map[common.Hash]common.Hash)
		for k, v := range account.Storage {
			storageCopy[k] = v
		}

		accountsCopy[addr] = stateAccount{
			Balance:     new(big.Int).Set(account.Balance),
			BalanceU256: uint256.NewInt(0).Set(account.BalanceU256),
			Nonce:       account.Nonce,
			Code:        append([]byte{}, account.Code...),
			CodeHash:    account.CodeHash,
			Storage:     storageCopy,
		}
	}

	// Copy suicided accounts
	suicidedCopy := make(map[common.Address]bool)
	for addr, status := range s.suicided {
		suicidedCopy[addr] = status
	}

	// Create and store the snapshot
	snap := &stateSnapshot{
		id:       id,
		accounts: accountsCopy,
		suicided: suicidedCopy,
	}

	s.snapshots = append(s.snapshots, snap)
	return id
}

// RevertToSnapshot reverts all state changes since the given snapshot
func (s *ImmuStateDB) RevertToSnapshot(id int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if id >= len(s.snapshots) {
		log.Error().Int("id", id).Int("snapshots", len(s.snapshots)).Msg("Attempted to revert to invalid snapshot")
		return
	}

	// Get the selected snapshot
	snapshot := s.snapshots[id]

	// Restore state from snapshot
	s.accounts = make(map[common.Address]*stateAccount)
	for addr, account := range snapshot.accounts {
		// Deep copy from snapshot to avoid shared references
		storageCopy := make(map[common.Hash]common.Hash)
		for k, v := range account.Storage {
			storageCopy[k] = v
		}

		accountCopy := &stateAccount{
			Balance:      new(big.Int).Set(account.Balance),
			BalanceU256:  uint256.NewInt(0).Set(account.BalanceU256),
			Nonce:        account.Nonce,
			Code:         append([]byte{}, account.Code...),
			CodeHash:     account.CodeHash,
			Storage:      storageCopy,
			StorageDirty: make(map[common.Hash]struct{}),
		}

		s.accounts[addr] = accountCopy
	}

	// Restore state objects
	s.stateObjects = make(map[common.Address]*stateObject)
	s.stateObjectsDirty = make(map[common.Address]struct{})

	for addr, account := range s.accounts {
		s.stateObjects[addr] = &stateObject{
			address: addr,
			account: *account,
			isDirty: false,
			deleted: false,
		}
	}

	// Restore suicided accounts
	s.suicided = make(map[common.Address]bool)
	for addr, status := range snapshot.suicided {
		s.suicided[addr] = status
	}

	// Remove snapshots after the current one
	s.snapshots = s.snapshots[:id]
}

// Log Methods
// ==========

// AddLog adds a log to the logs collection
func (s *ImmuStateDB) AddLog(log *types.Log) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.logs = append(s.logs, log)
	s.logSize += uint(len(log.Data))
	for _, topic := range log.Topics {
		s.logSize += uint(len(topic))
	}
}

// GetLogs returns all collected logs
func (s *ImmuStateDB) GetLogs(hash common.Hash, blockNumber uint64) []*types.Log {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	cpy := make([]*types.Log, len(s.logs))
	for i, log := range s.logs {
		// Update log with block details
		cpy[i] = &types.Log{
			Address:     log.Address,
			Topics:      log.Topics,
			Data:        log.Data,
			BlockNumber: blockNumber,
			TxHash:      hash,
			TxIndex:     uint(i),
			BlockHash:   hash,
			Index:       uint(i),
			Removed:     false,
		}
	}

	return cpy
}

// Auxiliary Methods
// ===============

// Exist checks whether an account exists at the given address
func (s *ImmuStateDB) Exist(addr common.Address) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.getStateObject(addr) != nil
}

// Empty checks whether an account is empty (nonce=0, balance=0, code=nil)
func (s *ImmuStateDB) Empty(addr common.Address) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	obj := s.getStateObject(addr)
	if obj == nil {
		return true
	}

	return obj.account.Nonce == 0 &&
		obj.account.Balance.Sign() == 0 &&
		len(obj.account.Code) == 0
}

// ForEachStorage iterates through the storage of an account
func (s *ImmuStateDB) ForEachStorage(addr common.Address, cb func(key, value common.Hash) bool) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	obj := s.getStateObject(addr)
	if obj == nil {
		return nil
	}

	// First process in-memory values
	for key, value := range obj.account.Storage {
		if !cb(key, value) {
			return nil
		}
	}

	// TODO: Load additional values from database if needed
	// This would require a storage prefix scan in the database

	return nil
}

// Commit Methods
// =============

// Finalise prepares the state for committing
func (s *ImmuStateDB) Finalise(deleteEmptyObjects bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for addr := range s.stateObjectsDirty {
		obj := s.stateObjects[addr]
		if obj.deleted {
			continue
		}

		// Delete empty objects if requested
		if deleteEmptyObjects && obj.account.Nonce == 0 &&
			obj.account.Balance.Sign() == 0 &&
			len(obj.account.Code) == 0 {
			obj.deleted = true
			continue
		}
	}
}

func (s *ImmuStateDB) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	if s.transientStorage == nil {
		return common.Hash{}
	}

	if storage, exists := s.transientStorage[addr]; exists {
		return storage[key]
	}
	return common.Hash{}
}

func (s *ImmuStateDB) SetTransientState(addr common.Address, key, value common.Hash) {
	if s.transientStorage == nil {
		s.transientStorage = make(map[common.Address]map[common.Hash]common.Hash)
	}

	if _, exists := s.transientStorage[addr]; !exists {
		s.transientStorage[addr] = make(map[common.Hash]common.Hash)
	}

	s.transientStorage[addr][key] = value
}

func (s *ImmuStateDB) HasSelfDestructed(addr common.Address) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.suicided[addr]
}

func (s *ImmuStateDB) SelfDestruct(addr common.Address) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Check if the account has already suicided
	if _, exists := s.suicided[addr]; exists {
		return
	}

	// Mark the account as suicided
	s.suicided[addr] = true
}

// Selfdestruct6780 implements EIP-6780 selfdestruct behavior
func (s *ImmuStateDB) Selfdestruct6780(addr common.Address) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.suicided[addr] = true
	obj := s.getOrCreateStateObject(addr)
	obj.deleted = true
	s.stateObjectsDirty[addr] = struct{}{}
}

func (s *ImmuStateDB) PointCache() *utils.PointCache {
	return s.pointCache
}

// Prepare implements the StateDB interface, preparing the state database for executing a transaction
func (s *ImmuStateDB) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Clear the transaction operations queue
	if len(s.txOps) > 0 {
		s.txOps = s.txOps[:0]
	}

	// Reset access lists for the transaction (EIP-2930)
	s.accessList = &accessList{
		addresses: make(map[common.Address]struct{}),
		slots:     make(map[common.Address]map[common.Hash]struct{}),
	}

	// Always include the sender in the access list
	s.accessList.addresses[sender] = struct{}{}

	// Include coinbase if required
	if rules.IsByzantium {
		s.accessList.addresses[coinbase] = struct{}{}
	}

	// Include destination address if it's a contract call
	if dest != nil {
		s.accessList.addresses[*dest] = struct{}{}
	}

	// Include precompiles if needed
	for _, addr := range precompiles {
		s.accessList.addresses[addr] = struct{}{}
	}

	// Add all addresses and slots from the transaction's access list if provided
	if txAccesses != nil {
		// Iterate through each AccessTuple in the list
		for _, tuple := range txAccesses {
			// Add the address
			s.AddAddressToAccessList(tuple.Address)

			// Add each storage key
			for _, key := range tuple.StorageKeys {
				s.AddSlotToAccessList(tuple.Address, key)
			}
		}
	}

	// Log transaction preparation
	log.Debug().
		Str("sender", sender.Hex()).
		Str("coinbase", coinbase.Hex()).
		Msg("State prepared for transaction execution")
}

func (s *ImmuStateDB) Witness() *stateless.Witness {
	s.witnessMutex.RLock()
	defer s.witnessMutex.RUnlock()

	return s.witness
}
