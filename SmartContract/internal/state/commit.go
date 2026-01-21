package state

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog/log"
)

// CommitToDB writes all changes to the database
func (s *ImmuStateDB) CommitToDB(deleteEmptyObjects bool) (common.Hash, error) {
	// Prepare state for committing
	s.Finalise(deleteEmptyObjects)

	s.mutex.Lock()
	defer s.mutex.Unlock()

	// If no DB client, we're done (in-memory mode) - but wait, we need to return a root hash?
	// For now let's calculate root anyway

	// Reset transaction batch
	s.txOps = s.txOps[:0]

	startTime := time.Now().UTC()

	// 1. Process account changes
	for addr := range s.stateObjectsDirty {
		obj := s.stateObjects[addr]

		// Handle deleted accounts
		if obj.deleted {
			if s.dbClient != nil {
				// Delete all account data
				s.queueDBOperation(getDBKey(prefixBalance, addr), nil, true)
				s.queueDBOperation(getDBKey(prefixNonce, addr), nil, true)
				s.queueDBOperation(getDBKey(prefixCode, addr), nil, true)
				s.queueDBOperation(getDBKey(prefixCodeHash, addr), nil, true)

				// Note: Ideally we should also delete all storage, but that requires iteration
			}
			delete(s.accounts, addr)
			delete(s.stateObjects, addr)
			continue
		}

		// Update account data
		if s.dbClient != nil {
			// Update balance
			balanceBytes := obj.account.Balance.Bytes()
			s.queueDBOperation(getDBKey(prefixBalance, addr), balanceBytes, false)

			// Update nonce
			// Simple serialization for nonce
			// In a real implementation use proper encoding
			nonceBytes := []byte(fmt.Sprintf("%d", obj.account.Nonce))
			s.queueDBOperation(getDBKey(prefixNonce, addr), nonceBytes, false)

			// Update code if changed
			if len(obj.account.Code) > 0 {
				s.queueDBOperation(getDBKey(prefixCode, addr), obj.account.Code, false)
				s.queueDBOperation(getDBKey(prefixCodeHash, addr), obj.account.CodeHash.Bytes(), false)
			}
		}

		// Update storage
		for key := range obj.account.StorageDirty {
			value := obj.account.Storage[key]
			if s.dbClient != nil {
				s.queueDBOperation(getDBKey(prefixStorage, addr, key), value.Bytes(), false)
			}
			delete(obj.account.StorageDirty, key)
		}
	}

	// 2. Commit remaining batch
	if s.dbClient != nil && len(s.txOps) > 0 {
		if err := s.commitBatch(); err != nil {
			return common.Hash{}, err
		}
	}

	// 3. Clear dirty sets
	s.stateObjectsDirty = make(map[common.Address]struct{})

	// 4. Calculate state root (simplified)
	// In a real implementation this would update the trie
	root := s.calculateStateRoot()
	s.stateRoot = root

	if s.dbClient != nil {
		log.Debug().
			Str("root", root.Hex()).
			Int("dirty_accounts", len(s.stateObjectsDirty)).
			Dur("duration_ms", time.Since(startTime)).
			Msg("State committed to database")
	}

	return root, nil
}

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

// Commit is a wrapper around CommitToDB for the StateDB interface
func (s *ImmuStateDB) Commit() (common.Hash, error) {
	return s.CommitToDB(true)
}

// Helper to calculate a simple state root hash
func (s *ImmuStateDB) calculateStateRoot() common.Hash {
	// Simple implementation: hash of all account addresses + balances + nonces
	// acceptable for now until full trie implementation
	h := crypto.NewKeccakState()

	// Improve determinism by iterating sorted addresses?
	// For now just hash what we have
	for addr, obj := range s.stateObjects {
		h.Write(addr.Bytes())
		h.Write(obj.account.Balance.Bytes())
		// Hash other fields...
	}

	var root common.Hash
	h.Read(root[:])
	return root
}
