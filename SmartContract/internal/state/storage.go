package state

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/DB_OPs"
)

// storage.go handles all storage-related operations

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
	if s.dbClient != nil {
		value, err := s.loadStorage(addr, key)
		if err != nil {
			return common.Hash{}
		}
		return value
	}

	return common.Hash{}
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

// loadStorage loads storage slots for an account
func (s *ImmuStateDB) loadStorage(addr common.Address, key common.Hash) (common.Hash, error) {
	storageKey := getDBKey(prefixStorage, addr, key)
	data, err := DB_OPs.Read(s.dbClient, storageKey)
	if err != nil {
		return common.Hash{}, nil // Not finding a storage slot is not an error
	}

	var value common.Hash
	if len(data) > 0 {
		copy(value[:], data)
	}

	// Update in-memory storage
	obj := s.getOrCreateStateObject(addr)
	obj.account.Storage[key] = value

	return value, nil
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

	return nil
}

// GetStorageRoot calculates the storage root for an account
func (s *ImmuStateDB) GetStorageRoot(addr common.Address) common.Hash {
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

// GetCommittedState gets the committed value of a storage slot
func (s *ImmuStateDB) GetCommittedState(addr common.Address, key common.Hash) common.Hash {
	// This is a simplified implementation that returns the current state
	// In a full implementation, you would distinguish between uncommitted and committed state
	return s.GetState(addr, key)
}

// LoadFromDatabase loads the entire state from the database
func (s *ImmuStateDB) LoadFromDatabase() error {
	// This would be used to initialize the state from a saved state root
	// For now, we just clear the state to start fresh
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.accounts = make(map[common.Address]*stateAccount)
	s.stateObjects = make(map[common.Address]*stateObject)
	s.stateObjectsDirty = make(map[common.Address]struct{})
	s.logs = make([]*types.Log, 0)
	s.snapshots = make([]*stateSnapshot, 0)
	s.suicided = make(map[common.Address]bool)

	// Try to load the latest state root
	if s.dbClient != nil {
		stateRootKey := fmt.Sprintf("%slatest", prefixStateRoot)
		rootData, err := DB_OPs.Read(s.dbClient, stateRootKey)
		if err == nil && len(rootData) == common.HashLength {
			copy(s.stateRoot[:], rootData)
		}
	}

	return nil
}
