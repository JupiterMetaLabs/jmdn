package state

import (
	"context"
	"encoding/json"
	"fmt"
	"gossipnode/SmartContract/internal/repository"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"

	pbdid "gossipnode/DID/proto"
)

// ContractDB implements vm.StateDB by using a stateObject model.
// It separates committed state (from DB_OPs) and transient state (in-memory changes).
// This enables atomic transaction processing with journal-based reverts.
type ContractDB struct {
	// Database connections
	didClient pbdid.DIDServiceClient     // DID Client for Balance/Nonce
	repo      repository.StateRepository // Generic Repository for code/storage

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

	// Context for storage metadata
	currentTxHash common.Hash
	currentBlock  uint64

	// Mutex for thread safety
	lock sync.RWMutex
}

// Ensure ContractDB implements vm.StateDB
var _ vm.StateDB = (*ContractDB)(nil)
var _ StateDB = (*ContractDB)(nil)

// Prefixes for database keys
var (
	PrefixCode         = []byte("code:")
	PrefixStorage      = []byte("storage:")
	PrefixNonce        = []byte("nonce:")
	PrefixContractMeta = []byte("meta:contract:")
	PrefixReceipt      = []byte("receipt:")
	PrefixStorageMeta  = []byte("meta:storage:")
)

// NewContractDB creates a new database interface for Smart Contracts.
func NewContractDB(didClient pbdid.DIDServiceClient, repo repository.StateRepository) *ContractDB {
	return &ContractDB{
		didClient:    didClient,
		repo:         repo,
		stateObjects: make(map[common.Address]*stateObject),
		journal:      newJournal(),
		logs:         make([]*types.Log, 0),
		accessList:   newAccessList(),
		currentBlock: 0, // Default
	}
}

// SetTxContext sets the current transaction context for storage metadata tracking.
func (c *ContractDB) SetTxContext(txHash common.Hash, blockNumber uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.currentTxHash = txHash
	c.currentBlock = blockNumber
}

// ============================================================================
// Persistence
// ============================================================================

// CommitToDB writes all dirty state changes to the database.
func (c *ContractDB) CommitToDB(deleteEmptyObjects bool) (common.Hash, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	batch := c.repo.NewBatch()
	defer batch.Close()

	// Iterate through all dirty state objects
	for addr, obj := range c.stateObjects {
		if !obj.isDirty() {
			continue
		}

		// Handle deleted/suicided accounts
		if deleteEmptyObjects && (obj.deleted || (obj.suicided && obj.isEmpty())) {
			// Delete code
			batch.DeleteCode(addr)

			// Delete all storage (this is simplified - in production we'd track all keys)
			// For now, we only delete what's in dirtyStorage
			for key := range obj.dirtyStorage {
				batch.DeleteStorage(addr, key)
			}

			// Delete nonce
			batch.DeleteNonce(addr)

			continue
		}

		// Commit storage changes
		toWrite, toDelete, metaUpdates := obj.finalizeStorage()
		for key, value := range toWrite {
			if err := batch.SaveStorage(addr, key, value); err != nil {
				return common.Hash{}, err
			}
		}

		// Commit storage metadata
		for key, meta := range metaUpdates {
			// Convert state.StorageMetadata to repository.StorageMetadata
			repoMeta := repository.StorageMetadata{
				ContractAddress:   meta.ContractAddress,
				StorageKey:        meta.StorageKey,
				ValueHash:         meta.ValueHash,
				LastModifiedBlock: meta.LastModifiedBlock,
				LastModifiedTx:    meta.LastModifiedTx,
				UpdatedAt:         meta.UpdatedAt,
			}
			if err := batch.SaveStorageMetadata(addr, key, repoMeta); err != nil {
				return common.Hash{}, err
			}
		}

		for _, key := range toDelete {
			if err := batch.DeleteStorage(addr, key); err != nil {
				return common.Hash{}, err
			}

			if err := batch.DeleteStorageMetadata(addr, key); err != nil {
				return common.Hash{}, err
			}
		}

		// Commit code if dirty
		if obj.dirtyCode {
			code := obj.getCode()
			fmt.Printf("DEBUG(contractsdb): CommitToDB handling dirtyCode for %s, len=%d\n", addr.Hex(), len(code))
			if len(code) == 0 {
				if err := batch.DeleteCode(addr); err != nil {
					return common.Hash{}, err
				}
			} else {
				if err := batch.SaveCode(addr, code); err != nil {
					return common.Hash{}, err
				}
			}
		}

		// Commit nonce (always save to local DB for consistency)
		if err := batch.SaveNonce(addr, obj.getNonce()); err != nil {
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

// GetBalanceChanges returns a map of all addresses that have had their balances modified
func (c *ContractDB) GetBalanceChanges() map[common.Address]*uint256.Int {
	c.lock.RLock()
	defer c.lock.RUnlock()

	changes := make(map[common.Address]*uint256.Int)
	for addr, obj := range c.stateObjects {
		if !obj.isDirty() {
			continue
		}

		if obj.data.Balance == nil || obj.originAccount.Balance == nil {
			continue
		}

		if obj.data.Balance.Cmp(obj.originAccount.Balance) != 0 {
			changes[addr] = new(uint256.Int).Set(obj.data.Balance)
		}
	}
	return changes
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

// AccessEvents returns the state access events for the transaction.
func (c *ContractDB) AccessEvents() *state.AccessEvents { return nil }

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



// GetStateAndCommittedState returns both the current and committed state in a single call.
func (c *ContractDB) GetStateAndCommittedState(addr common.Address, key common.Hash) (common.Hash, common.Hash) {
	return c.GetState(addr, key), c.GetCommittedState(addr, key)
}

// IsNewContract returns true if the contract was created in this transaction.
func (c *ContractDB) IsNewContract(addr common.Address) bool {
	// Not implemented tracking for this yet.
	return false
}

// GetTransientState returns the transient storage value (EIP-1153).
func (c *ContractDB) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return common.Hash{}
}

// SetTransientState sets the transient storage value (EIP-1153).
func (c *ContractDB) SetTransientState(addr common.Address, key, value common.Hash) {}



// GetStorageRoot returns the storage root for an address.
func (c *ContractDB) GetStorageRoot(addr common.Address) common.Hash { return common.Hash{} }

// GetSelfDestruction returns whether an address self-destructed.
func (c *ContractDB) GetSelfDestruction(addr common.Address) bool { return false }

// ============================================================================
// Internal Helpers
// ============================================================================

// ============================================================================
// Internal Helpers - Deprecated
// ============================================================================
// DB keys are now handled by the repository adapters.

// ============================================================================
// Metadata & Receipt Persistence
// ============================================================================

// SetContractMetadata stores the metadata for a deployed contract
func (c *ContractDB) SetContractMetadata(addr common.Address, meta ContractMetadata) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal contract metadata: %w", err)
	}
	// We must use a StateBatch to save to the database now
	batch := c.repo.NewBatch()
	defer batch.Close()

	if err := batch.SaveContractMetadata(addr, data); err != nil {
		return err
	}
	return batch.Commit()
}

// GetContractMetadata retrieves the metadata for a contract
func (c *ContractDB) GetContractMetadata(addr common.Address) (*ContractMetadata, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	data, err := c.repo.GetContractMetadata(context.Background(), addr)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil // Not found
	}

	var meta ContractMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal contract metadata: %w", err)
	}

	return &meta, nil
}

// WriteReceipt stores the transaction receipt
func (c *ContractDB) WriteReceipt(receipt TransactionReceipt) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("failed to marshal receipt: %w", err)
	}

	batch := c.repo.NewBatch()
	defer batch.Close()

	if err := batch.SaveReceipt(receipt.TxHash, data); err != nil {
		return err
	}
	return batch.Commit()
}

// GetReceipt retrieves a transaction receipt by hash
func (c *ContractDB) GetReceipt(txHash common.Hash) (*TransactionReceipt, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	data, err := c.repo.GetReceipt(context.Background(), txHash)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}

	var receipt TransactionReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
	}

	return &receipt, nil
}
