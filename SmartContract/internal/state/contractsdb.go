package state

import (
	"fmt"
	"math/big"
	"sync"

	"gossipnode/SmartContract/internal/storage"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie/utils"

	pbdid "gossipnode/DID/proto"
)

// ContractDB implements vm.StateDB by using a stateObject model.
// It separates committed state (from DB_OPs) and transient state (in-memory changes).
// This enables atomic transaction processing with journal-based reverts.
type ContractDB struct {
	// Database connections
	didClient pbdid.DIDServiceClient // DID Client for Balance/Nonce
	db        storage.KVStore        // PebbleDB for code/storage

	// State object cache (one per address accessed)
	stateObjects map[common.Address]*stateObject

	// Journal for reverts
	journal *journal

	// Gas refund counter
	refund uint64

	// Pending logs (events emitted by contracts)
	logs []*types.Log

	// Access list (EIP-2930)
	accessList *accessList

	// Mutex for thread safety
	lock sync.RWMutex
}

// Ensure ContractDB implements vm.StateDB
var _ vm.StateDB = (*ContractDB)(nil)
var _ StateDB = (*ContractDB)(nil)

// Prefixes for database keys
var (
	PrefixCode    = []byte("code:")
	PrefixStorage = []byte("storage:")
	PrefixNonce   = []byte("nonce:")
)

// NewContractDB creates a new database interface for Smart Contracts.
func NewContractDB(didClient pbdid.DIDServiceClient, db storage.KVStore) *ContractDB {
	return &ContractDB{
		didClient:    didClient,
		db:           db,
		stateObjects: make(map[common.Address]*stateObject),
		journal:      newJournal(),
		logs:         make([]*types.Log, 0),
		accessList:   newAccessList(),
	}
}

// ============================================================================
// Persistence
// ============================================================================

// CommitToDB writes all dirty state changes to the database.
func (c *ContractDB) CommitToDB(deleteEmptyObjects bool) (common.Hash, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	batch := c.db.NewBatch()
	defer batch.Close()

	// Iterate through all dirty state objects
	for addr, obj := range c.stateObjects {
		if !obj.isDirty() {
			continue
		}

		// Handle deleted/suicided accounts
		if deleteEmptyObjects && (obj.deleted || (obj.suicided && obj.isEmpty())) {
			// Delete code
			codeKey := makeCodeKey(addr)
			batch.Delete(codeKey)

			// Delete all storage (this is simplified - in production we'd track all keys)
			// For now, we only delete what's in dirtyStorage
			for key := range obj.dirtyStorage {
				storageKey := makeStorageKey(addr, key)
				batch.Delete(storageKey)
			}

			// Delete nonce
			nonceKey := makeNonceKey(addr)
			batch.Delete(nonceKey)

			continue
		}

		// Commit storage changes
		toWrite, toDelete := obj.finalizeStorage()
		for key, value := range toWrite {
			dbKey := makeStorageKey(addr, key)
			if err := batch.Set(dbKey, value[:]); err != nil {
				return common.Hash{}, err
			}
		}
		for _, key := range toDelete {
			dbKey := makeStorageKey(addr, key)
			if err := batch.Delete(dbKey); err != nil {
				return common.Hash{}, err
			}
		}

		// Commit code if dirty
		if obj.dirtyCode {
			code := obj.getCode()
			codeKey := makeCodeKey(addr)
			if len(code) == 0 {
				if err := batch.Delete(codeKey); err != nil {
					return common.Hash{}, err
				}
			} else {
				if err := batch.Set(codeKey, code); err != nil {
					return common.Hash{}, err
				}
			}
		}

		// Commit nonce (always save to local DB for consistency)
		nonceKey := makeNonceKey(addr)
		nonceBytes := new(big.Int).SetUint64(obj.getNonce()).Bytes()
		if err := batch.Set(nonceKey, nonceBytes); err != nil {
			return common.Hash{}, err
		}

		// Mark object as committed
		obj.commitState()
	}

	// Write batch to database
	if err := batch.Commit(); err != nil {
		return common.Hash{}, err
	}

	// Clear journal after successful commit
	c.journal = newJournal()

	// We don't calculate state root for now
	return common.Hash{}, nil
}

// Finalise is called after transaction execution to finalize state changes.
func (c *ContractDB) Finalise(deleteEmptyObjects bool) {
	// In the new model, we don't need to do anything here
	// All changes are tracked in stateObjects and journal
}

// ============================================================================
// EVM Mechanics
// ============================================================================

// AddRefund adds gas to the refund counter.
func (c *ContractDB) AddRefund(gas uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()

	prev := c.refund
	c.journal.append(refundChange{prev: prev})
	c.refund += gas
}

// SubRefund subtracts gas from the refund counter.
func (c *ContractDB) SubRefund(gas uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if gas > c.refund {
		fmt.Printf("⚠️  SubRefund UNDERFLOW prevented! Current: %d, Sub: %d\n", c.refund, gas)
		c.refund = 0
		return
	}
	c.refund -= gas
}

// GetRefund returns the current gas refund counter.
func (c *ContractDB) GetRefund() uint64 {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.refund
}

// AddLog adds a log entry.
func (c *ContractDB) AddLog(log *types.Log) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.journal.append(addLogChange{txhash: log.TxHash})
	c.logs = append(c.logs, log)
}

// GetLogs returns logs matching the given criteria.
func (c *ContractDB) GetLogs(hash common.Hash, blockNumber uint64, hash2 common.Hash) []*types.Log {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.logs
}

// Logs returns all logs.
func (c *ContractDB) Logs() []*types.Log {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.logs
}

// ============================================================================
// Required Interface Methods
// ============================================================================

// Witness returns the witness for stateless execution.
func (c *ContractDB) Witness() *stateless.Witness { return nil }

// Prepare handles the preparation of access lists for EIP-2929/2930.
func (c *ContractDB) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList) {
	// Initialize access lists if needed
}

// AddressInAccessList checks if an address is in the access list.
func (c *ContractDB) AddressInAccessList(addr common.Address) bool {
	return c.accessList.ContainsAddress(addr)
}

// SlotInAccessList checks if a storage slot is in the access list.
func (c *ContractDB) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	return c.accessList.Contains(addr, slot)
}

// AddAddressToAccessList adds an address to the access list.
func (c *ContractDB) AddAddressToAccessList(addr common.Address) {
	c.journal.append(accessListAddAccountChange{address: &addr})
	c.accessList.AddAddress(addr)
}

// AddSlotToAccessList adds a storage slot to the access list.
func (c *ContractDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	c.journal.append(accessListAddSlotChange{
		address: &addr,
		slot:    &slot,
	})
	c.accessList.AddSlot(addr, slot)
}

// AddPreimage adds a preimage for a hash (not implemented).
func (c *ContractDB) AddPreimage(hash common.Hash, preimage []byte) {}

// ForEachStorage iterates over all storage slots for an address.
func (c *ContractDB) ForEachStorage(addr common.Address, cb func(key, value common.Hash) bool) error {
	return nil
}

// GetTransientState returns the transient storage value (EIP-1153).
func (c *ContractDB) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return common.Hash{}
}

// SetTransientState sets the transient storage value (EIP-1153).
func (c *ContractDB) SetTransientState(addr common.Address, key, value common.Hash) {}

// PointCache returns the point cache (for verkle trees).
func (c *ContractDB) PointCache() *utils.PointCache { return nil }

// GetStorageRoot returns the storage root for an address.
func (c *ContractDB) GetStorageRoot(addr common.Address) common.Hash { return common.Hash{} }

// GetSelfDestruction returns whether an address self-destructed.
func (c *ContractDB) GetSelfDestruction(addr common.Address) bool { return false }

// ============================================================================
// Internal Helpers
// ============================================================================

func makeStorageKey(addr common.Address, key common.Hash) []byte {
	return append(PrefixStorage, append(addr.Bytes(), key.Bytes()...)...)
}

func makeCodeKey(addr common.Address) []byte {
	return append(PrefixCode, addr.Bytes()...)
}

func makeNonceKey(addr common.Address) []byte {
	return append(PrefixNonce, addr.Bytes()...)
}
