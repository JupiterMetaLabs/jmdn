package state

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/rs/zerolog/log"
)

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
			Balance:      new(big.Int).Set(account.Balance),
			BalanceU256:  uint256.NewInt(0).Set(account.BalanceU256),
			Nonce:        account.Nonce,
			Code:         append([]byte{}, account.Code...),
			CodeHash:     account.CodeHash,
			Storage:      storageCopy,
			StorageDirty: make(map[common.Hash]struct{}),
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
