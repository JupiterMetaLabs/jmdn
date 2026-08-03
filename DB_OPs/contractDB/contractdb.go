package contractDB

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pbdid "gossipnode/DID/proto"
)

// ============================================================================
// StateDB interface (public — extend vm.StateDB with JMDN-specific methods)
// ============================================================================

// StateDB extends go-ethereum's vm.StateDB with persistence and balance-tracking
// methods required by the JMDN node.
type StateDB interface {
	vm.StateDB

	// CommitToDB writes all pending state changes to the underlying database.
	// If deleteEmptyObjects is true, accounts that become empty are removed.
	CommitToDB(deleteEmptyObjects bool) (common.Hash, error)

	// Finalise finalises state changes for the current transaction without
	// persisting them. Called at the end of each EVM transaction.
	Finalise(deleteEmptyObjects bool)

	// GetBalanceChanges returns every address whose balance changed this
	// transaction and its new balance.
	GetBalanceChanges() map[common.Address]*uint256.Int
}

// ============================================================================
// ContractDB — the vm.StateDB implementation
// ============================================================================

// ContractDB implements vm.StateDB using a stateObject model backed by PebbleDB
// (via StateRepository) for code/storage and the JMDN DID service for balances/nonces.
type ContractDB struct {
	// Persistence backends
	didClient pbdid.DIDServiceClient // DID service — balance and nonce source of truth
	repo      StateRepository        // Local PebbleDB — code, storage, receipts, metadata

	// In-memory account cache
	stateObjects map[common.Address]*stateObject

	// Snapshot / revert support
	journal *journal

	// EVM execution state
	refund     uint64
	logs       []*types.Log
	accessList *accessList

	// Current transaction context (for storage metadata timestamps)
	currentTxHash common.Hash
	currentBlock  uint64

	lock sync.RWMutex
}

// Ensure ContractDB satisfies both interfaces at compile time.
var _ vm.StateDB = (*ContractDB)(nil)
var _ StateDB = (*ContractDB)(nil)

// NewContractDB creates a ContractDB instance ready for EVM execution.
func NewContractDB(didClient pbdid.DIDServiceClient, repo StateRepository) *ContractDB {
	return &ContractDB{
		didClient:    didClient,
		repo:         repo,
		stateObjects: make(map[common.Address]*stateObject),
		journal:      newJournal(),
		logs:         make([]*types.Log, 0),
		accessList:   newAccessList(),
	}
}

// SetTxContext updates the transaction hash and block number used when recording
// storage metadata. Call this at the start of each transaction.
func (c *ContractDB) SetTxContext(txHash common.Hash, blockNumber uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.currentTxHash = txHash
	c.currentBlock = blockNumber
}

// ============================================================================
// Persistence
// ============================================================================

// CommitToDB writes all dirty state to PebbleDB atomically.
func (c *ContractDB) CommitToDB(deleteEmptyObjects bool) (common.Hash, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	batch := c.repo.NewBatch()
	defer batch.Close()

	for addr, obj := range c.stateObjects {
		if !obj.isDirty() {
			continue
		}

		if deleteEmptyObjects && (obj.deleted || (obj.suicided && obj.isEmpty())) {
			batch.DeleteCode(addr)
			for key := range obj.dirtyStorage {
				batch.DeleteStorage(addr, key)
			}
			batch.DeleteNonce(addr)
			continue
		}

		toWrite, toDelete, metaUpdates := obj.finalizeStorage()

		for key, value := range toWrite {
			if err := batch.SaveStorage(addr, key, value); err != nil {
				return common.Hash{}, err
			}
		}
		for key, meta := range metaUpdates {
			if err := batch.SaveStorageMetadata(addr, key, meta); err != nil {
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

		if obj.dirtyCode {
			code := obj.getCode()
			if l := logger(); l != nil {
				l.Debug(context.Background(), "CommitToDB: writing code",
					ion.String("addr", addr.Hex()),
					ion.Int("code_len", len(code)),
				)
			}
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

		if obj.dirtyNonce {
			if err := batch.SaveNonce(addr, obj.getNonce()); err != nil {
				return common.Hash{}, err
			}
		}

		obj.commitState()
	}

	if err := batch.Commit(); err != nil {
		return common.Hash{}, err
	}

	c.journal = newJournal()
	return common.Hash{}, nil // state root not computed
}

// GetBalanceChanges returns addresses whose balance changed and their new values.
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

// Finalise is called after each EVM transaction to mark the end of that transaction's state.
func (c *ContractDB) Finalise(deleteEmptyObjects bool) {}

// ============================================================================
// Metadata & Receipt persistence
// ============================================================================

// SetContractMetadata stores deployment metadata for a contract address.
func (c *ContractDB) SetContractMetadata(addr common.Address, meta ContractMetadata) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal contract metadata: %w", err)
	}
	batch := c.repo.NewBatch()
	defer batch.Close()
	if err := batch.SaveContractMetadata(addr, data); err != nil {
		return err
	}
	return batch.Commit()
}

// GetContractMetadata retrieves deployment metadata for a contract address.
func (c *ContractDB) GetContractMetadata(addr common.Address) (*ContractMetadata, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	data, err := c.repo.GetContractMetadata(context.Background(), addr)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var meta ContractMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal contract metadata: %w", err)
	}
	return &meta, nil
}

// WriteReceipt stores a transaction receipt.
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

// GetReceipt retrieves a transaction receipt by its hash.
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

// ============================================================================
// EVM Mechanics
// ============================================================================

func (c *ContractDB) AddRefund(gas uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.journal.append(refundChange{prev: c.refund})
	c.refund += gas
}

func (c *ContractDB) SubRefund(gas uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if gas > c.refund {
		if l := logger(); l != nil {
			l.Warn(context.Background(), "SubRefund underflow prevented, clamping to 0",
				ion.Uint64("current_refund", c.refund),
				ion.Uint64("sub_amount", gas),
			)
		}
		c.refund = 0
		return
	}
	c.refund -= gas
}

func (c *ContractDB) GetRefund() uint64 {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.refund
}

func (c *ContractDB) AddLog(log *types.Log) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.journal.append(addLogChange{txhash: log.TxHash})
	c.logs = append(c.logs, log)
}

func (c *ContractDB) GetLogs(_ common.Hash, _ uint64, _ common.Hash) []*types.Log {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.logs
}

// Logs returns all logs captured since the last reset.
func (c *ContractDB) Logs() []*types.Log {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.logs
}

// ============================================================================
// Required vm.StateDB interface stubs
// ============================================================================

func (c *ContractDB) Witness() *stateless.Witness { return nil }

func (c *ContractDB) AccessEvents() *state.AccessEvents { return nil }

func (c *ContractDB) Prepare(_ params.Rules, _, _ common.Address, _ *common.Address, _ []common.Address, _ types.AccessList) {
}

func (c *ContractDB) AddressInAccessList(addr common.Address) bool {
	return c.accessList.ContainsAddress(addr)
}

func (c *ContractDB) SlotInAccessList(addr common.Address, slot common.Hash) (bool, bool) {
	return c.accessList.Contains(addr, slot)
}

func (c *ContractDB) AddAddressToAccessList(addr common.Address) {
	c.journal.append(accessListAddAccountChange{address: &addr})
	c.accessList.AddAddress(addr)
}

func (c *ContractDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	c.journal.append(accessListAddSlotChange{address: &addr, slot: &slot})
	c.accessList.AddSlot(addr, slot)
}

func (c *ContractDB) AddPreimage(_ common.Hash, _ []byte) {}

func (c *ContractDB) ForEachStorage(_ common.Address, _ func(key, value common.Hash) bool) error {
	return nil
}

func (c *ContractDB) GetStateAndCommittedState(addr common.Address, key common.Hash) (common.Hash, common.Hash) {
	return c.GetState(addr, key), c.GetCommittedState(addr, key)
}

func (c *ContractDB) IsNewContract(_ common.Address) bool { return false }
func (c *ContractDB) GetTransientState(_ common.Address, _ common.Hash) common.Hash {
	return common.Hash{}
}
func (c *ContractDB) SetTransientState(_ common.Address, _, _ common.Hash) {}
func (c *ContractDB) GetStorageRoot(_ common.Address) common.Hash          { return common.Hash{} }
func (c *ContractDB) GetSelfDestruction(_ common.Address) bool             { return false }

// ============================================================================
// Lightweight helpers (use shared singletons directly — no full StateDB needed)
// ============================================================================

// HasCode returns true if the given address has contract bytecode stored in the
// shared KVStore.  This is a cheap read-only check that avoids the overhead of
// spinning up a full ContractDB / StateDB.  Returns false on any error.
func HasCode(addr common.Address) bool {
	if sharedKVStore == nil {
		return false
	}
	val, err := sharedKVStore.Get(makeCodeKey(addr))
	return err == nil && len(val) > 0
}

// GetCodeBytes returns the raw bytecode for a contract from the shared KVStore.
// Returns (nil, false) when the KVStore is uninitialised or no code is found.
// Used by the pull-on-demand responder to transfer bytecode to requesting nodes.
func GetCodeBytes(addr common.Address) ([]byte, bool) {
	if sharedKVStore == nil {
		return nil, false
	}
	val, err := sharedKVStore.Get(makeCodeKey(addr))
	if err != nil || len(val) == 0 {
		return nil, false
	}
	return val, true
}

// StoreCodeBytes writes raw bytecode for a contract into the shared KVStore.
// Used by the pull-on-demand client to persist bytecode received from a peer.
// No-op if the KVStore is uninitialised.
func StoreCodeBytes(addr common.Address, code []byte) error {
	if sharedKVStore == nil {
		return fmt.Errorf("contractDB: KVStore not initialised, cannot store code for %s", addr.Hex())
	}
	if len(code) == 0 {
		return nil
	}
	return sharedKVStore.Set(makeCodeKey(addr), code)
}

// ============================================================================
// Process-wide singletons (set at startup by server_integration.go / cmd/main.go)
// ============================================================================

// sharedKVStore is the singleton KVStore shared across all EVM executions in this process.
// It prevents multiple PebbleDB file locks from being acquired.
var sharedKVStore KVStore

// SetSharedKVStore stores the process-wide KVStore singleton.
// Must be called once at startup before any EVM processing begins.
func SetSharedKVStore(store KVStore) {
	sharedKVStore = store
}

// sharedStateRepo is the singleton StateRepository used by request-scoped
// InitializeStateDB calls (router handlers, estimators, tracers).
var sharedStateRepo StateRepository

// SetSharedStateRepository stores the process-wide StateRepository singleton.
// Must be configured during startup before any EVM processing begins.
func SetSharedStateRepository(repo StateRepository) {
	sharedStateRepo = repo
}

// sharedDIDClient is the singleton gRPC client for the DID service.
// Reusing one connection avoids dialling per deployment.
var sharedDIDClient pbdid.DIDServiceClient

// SetSharedDIDClient stores the process-wide DID gRPC client singleton.
// Must be called once at startup (by server_integration.go or cmd/main.go).
func SetSharedDIDClient(client pbdid.DIDServiceClient) {
	sharedDIDClient = client
}

// InitializeStateDB creates a new StateDB instance for EVM execution.
// It reuses the process-wide singletons where available and falls back to
// env-var-configured connections for standalone / test use.
func InitializeStateDB() (StateDB, error) {
	// Resolve DID client
	var didClient pbdid.DIDServiceClient
	if sharedDIDClient != nil {
		didClient = sharedDIDClient
	} else {
		didAddr := "localhost:15052"
		if addr := os.Getenv("JMDN_PORTS_DID_ADDR"); addr != "" {
			didAddr = addr
		}
		didConn, connErr := grpc.NewClient(didAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if connErr != nil {
			return nil, fmt.Errorf("failed to connect to DID service at %s: %w", didAddr, connErr)
		}
		didClient = pbdid.NewDIDServiceClient(didConn)
	}

	// Resolve StateRepository
	if sharedStateRepo == nil {
		return nil, fmt.Errorf("contractDB: StateRepository not initialised (Thebe repository is required)")
	}

	return NewContractDB(didClient, sharedStateRepo), nil
}
