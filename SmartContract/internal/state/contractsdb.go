package state

import (
	"context"
	"fmt"
	"math/big"
	"sync"

	"gossipnode/SmartContract/internal/storage"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie/utils"
	"github.com/holiman/uint256"

	pb "gossipnode/gETH/proto"
)

// ContractDB implements vm.StateDB by proxying account reads to a remote gETH node
// and storing contract state locally in PebbleDB.
// It is specifically designed for the SmartContract module to manage contract data.
type ContractDB struct {
	client pb.ChainClient
	db     storage.KVStore

	// Caches and Dirty sets for Contract Storage
	storage      map[common.Address]map[common.Hash]common.Hash
	storageDirty map[common.Address]map[common.Hash]struct{}

	// Caches and Dirty sets for Contract Code
	code      map[common.Address][]byte
	codeDirty map[common.Address]struct{}

	// Pending Logs (Events emitted by contracts)
	logs []*types.Log

	// Snapshots for EVM Reverts
	snapshots []ContractSnapshot

	// Access List (EIP-2930)
	accessList *accessList

	// Refunds (Gas refunds)
	refund uint64

	// Mutex for thread safety
	lock sync.RWMutex
}

type ContractSnapshot struct {
	id      int
	storage map[common.Address]map[common.Hash]common.Hash
	code    map[common.Address][]byte
	logs    []*types.Log
	refund  uint64
}

// Ensure ContractDB implements vm.StateDB
var _ vm.StateDB = (*ContractDB)(nil)
var _ StateDB = (*ContractDB)(nil)

// Prefixes for database keys
var (
	PrefixCode    = []byte("code:")
	PrefixStorage = []byte("storage:")
)

// NewContractDB creates a new database interface for Smart Contracts
func NewContractDB(client pb.ChainClient, db storage.KVStore) *ContractDB {
	return &ContractDB{
		client:       client,
		db:           db,
		storage:      make(map[common.Address]map[common.Hash]common.Hash),
		storageDirty: make(map[common.Address]map[common.Hash]struct{}),
		code:         make(map[common.Address][]byte),
		codeDirty:    make(map[common.Address]struct{}),
		logs:         make([]*types.Log, 0),
		snapshots:    make([]ContractSnapshot, 0),
		accessList:   newAccessList(),
	}
}

// ============================================================================
// Account State (Proxied to gETH)
// ============================================================================

func (c *ContractDB) CreateAccount(addr common.Address) {
	// Virtual creation - The main chain handles the actual account creation logic.
}

func (c *ContractDB) CreateContract(addr common.Address) {
	c.CreateAccount(addr)
}

func (c *ContractDB) SubBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) {
	// Balance changes are handled by the main chain.
	// We might need to track this if we were doing block production, but for just executing contracts
	// locally or validating, we might not need to write back to main chain immediately via this method.
}

func (c *ContractDB) AddBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) {
	// Same as SubBalance.
}

func (c *ContractDB) GetBalance(addr common.Address) *uint256.Int {
	req := &pb.GetAccountStateReq{Address: addr.Bytes()}
	resp, err := c.client.GetAccountState(context.Background(), req)
	if err != nil {
		fmt.Printf("Error fetching balance for %s: %v\n", addr.Hex(), err)
		return uint256.NewInt(0)
	}

	bal := new(uint256.Int)
	if len(resp.Balance) > 0 {
		bal.SetBytes(resp.Balance)
	}
	return bal
}

func (c *ContractDB) GetNonce(addr common.Address) uint64 {
	req := &pb.GetAccountStateReq{Address: addr.Bytes()}
	resp, err := c.client.GetAccountState(context.Background(), req)
	if err != nil || len(resp.Nonce) == 0 {
		return 0
	}
	return new(big.Int).SetBytes(resp.Nonce).Uint64()
}

func (c *ContractDB) SetNonce(addr common.Address, nonce uint64) {
	// Local cache/noop
}

func (c *ContractDB) GetCodeHash(addr common.Address) common.Hash {
	// Check cached/local code first
	code := c.GetCode(addr)
	if len(code) > 0 {
		return crypto.Keccak256Hash(code)
	}

	req := &pb.GetAccountStateReq{Address: addr.Bytes()}
	resp, err := c.client.GetAccountState(context.Background(), req)
	if err != nil {
		return common.Hash{}
	}
	return common.BytesToHash(resp.CodeHash)
}

func (c *ContractDB) GetCode(addr common.Address) []byte {
	c.lock.RLock()
	if code, ok := c.code[addr]; ok {
		c.lock.RUnlock()
		return code
	}
	c.lock.RUnlock()

	// Check Persistent DB
	key := makeCodeKey(addr)
	if val, err := c.db.Get(key); err == nil && len(val) > 0 {
		return val
	}

	req := &pb.GetAccountStateReq{Address: addr.Bytes()}
	resp, err := c.client.GetAccountState(context.Background(), req)
	if err != nil {
		return nil
	}
	return resp.Code
}

func (c *ContractDB) SetCode(addr common.Address, code []byte) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.code[addr] = code
	c.codeDirty[addr] = struct{}{}
}

func (c *ContractDB) GetCodeSize(addr common.Address) int {
	code := c.GetCode(addr)
	return len(code)
}

// ============================================================================
// Contract Storage (Persisted in Pebble)
// ============================================================================

func (c *ContractDB) GetState(addr common.Address, key common.Hash) common.Hash {
	c.lock.RLock()
	// Check memory cache first
	if val, ok := c.storage[addr][key]; ok {
		c.lock.RUnlock()
		return val
	}
	c.lock.RUnlock()

	// Check KVStore
	dbKey := makeStorageKey(addr, key)
	val, err := c.db.Get(dbKey)
	if err != nil {
		return common.Hash{}
	}

	// Val is already a copy or nil from adapter
	if len(val) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(val)
}

func (c *ContractDB) SetState(addr common.Address, key common.Hash, value common.Hash) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.storage[addr] == nil {
		c.storage[addr] = make(map[common.Hash]common.Hash)
		c.storageDirty[addr] = make(map[common.Hash]struct{})
	}
	c.storage[addr][key] = value
	c.storageDirty[addr][key] = struct{}{}
}

func (c *ContractDB) GetCommittedState(addr common.Address, key common.Hash) common.Hash {
	return c.GetState(addr, key)
}

// ============================================================================
// Persistence
// ============================================================================

func (c *ContractDB) CommitToDB(deleteEmptyObjects bool) (common.Hash, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	batch := c.db.NewBatch()
	defer batch.Close()

	for addr, dirtyKeys := range c.storageDirty {
		for key := range dirtyKeys {
			value := c.storage[addr][key]
			dbKey := makeStorageKey(addr, key)

			if value == (common.Hash{}) {
				if err := batch.Delete(dbKey); err != nil {
					return common.Hash{}, err
				}
			} else {
				if err := batch.Set(dbKey, value[:]); err != nil {
					return common.Hash{}, err
				}
			}
		}
		c.storageDirty[addr] = make(map[common.Hash]struct{})
	}

	// Commit Code
	for addr := range c.codeDirty {
		code := c.code[addr]
		dbKey := makeCodeKey(addr)

		if len(code) == 0 {
			if err := batch.Delete(dbKey); err != nil {
				return common.Hash{}, err
			}
		} else {
			if err := batch.Set(dbKey, code); err != nil {
				return common.Hash{}, err
			}
		}
	}
	// Clear code dirty map.
	// We re-allocate to clear it safely while lock is held (defer unlock).
	c.codeDirty = make(map[common.Address]struct{})

	if err := batch.Commit(); err != nil {
		return common.Hash{}, err
	}

	// We don't verify state root for local contract storage currently
	return common.Hash{}, nil
}

func (c *ContractDB) Finalise(deleteEmptyObjects bool) {
	// No-op
}

// ============================================================================
// EVM Mechanics
// ============================================================================

func (c *ContractDB) AddRefund(gas uint64) {
	c.refund += gas
}

func (c *ContractDB) SubRefund(gas uint64) {
	c.refund -= gas
}

func (c *ContractDB) GetRefund() uint64 {
	return c.refund
}

func (c *ContractDB) AddLog(log *types.Log) {
	c.logs = append(c.logs, log)
}

func (c *ContractDB) GetLogs(hash common.Hash, blockNumber uint64, hash2 common.Hash) []*types.Log {
	return c.logs
}

func (c *ContractDB) Logs() []*types.Log {
	return c.logs
}

func (c *ContractDB) Snapshot() int {
	id := len(c.snapshots)
	snap := ContractSnapshot{
		id:      id,
		storage: copyStorage(c.storage),
		code:    copyCode(c.code),
		logs:    make([]*types.Log, len(c.logs)),
		refund:  c.refund,
	}
	copy(snap.logs, c.logs)
	c.snapshots = append(c.snapshots, snap)
	return id
}

func (c *ContractDB) RevertToSnapshot(revid int) {
	if revid >= len(c.snapshots) {
		return
	}
	snap := c.snapshots[revid]
	c.storage = snap.storage
	c.code = snap.code
	c.logs = snap.logs
	c.refund = snap.refund
	c.snapshots = c.snapshots[:revid]
}

// Witness returns the witness for statutory execution
func (c *ContractDB) Witness() *stateless.Witness { return nil }

func (c *ContractDB) Suicide(addr common.Address) bool           { return true }
func (c *ContractDB) SelfDestruct(addr common.Address)           { c.Suicide(addr) }
func (c *ContractDB) HasSuicided(addr common.Address) bool       { return false }
func (c *ContractDB) HasSelfDestructed(addr common.Address) bool { return c.HasSuicided(addr) }
func (c *ContractDB) Selfdestruct6780(addr common.Address)       { c.Suicide(addr) }
func (c *ContractDB) Exist(addr common.Address) bool {
	bal := c.GetBalance(addr)
	return bal.Sign() > 0 || c.GetNonce(addr) > 0 || len(c.GetCode(addr)) > 0
}
func (c *ContractDB) Empty(addr common.Address) bool { return !c.Exist(addr) }
func (c *ContractDB) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList) {
}
func (c *ContractDB) AddressInAccessList(addr common.Address) bool {
	return c.accessList.ContainsAddress(addr)
}
func (c *ContractDB) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	return c.accessList.Contains(addr, slot)
}
func (c *ContractDB) AddAddressToAccessList(addr common.Address) { c.accessList.AddAddress(addr) }
func (c *ContractDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	c.accessList.AddSlot(addr, slot)
}
func (c *ContractDB) AddPreimage(hash common.Hash, preimage []byte) {}
func (c *ContractDB) ForEachStorage(addr common.Address, cb func(key, value common.Hash) bool) error {
	return nil
}
func (c *ContractDB) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return common.Hash{}
}
func (c *ContractDB) SetTransientState(addr common.Address, key, value common.Hash) {}
func (c *ContractDB) PointCache() *utils.PointCache                                 { return nil }

// Internal Helpers

func makeStorageKey(addr common.Address, key common.Hash) []byte {
	return append(PrefixStorage, append(addr.Bytes(), key.Bytes()...)...)
}

func makeCodeKey(addr common.Address) []byte {
	return append(PrefixCode, addr.Bytes()...)
}

func copyCode(src map[common.Address][]byte) map[common.Address][]byte {
	dst := make(map[common.Address][]byte)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func copyStorage(src map[common.Address]map[common.Hash]common.Hash) map[common.Address]map[common.Hash]common.Hash {
	dst := make(map[common.Address]map[common.Hash]common.Hash)
	for addr, store := range src {
		dst[addr] = make(map[common.Hash]common.Hash)
		for k, v := range store {
			dst[addr][k] = v
		}
	}
	return dst
}

type accessList struct {
	addresses map[common.Address]struct{}
	slots     map[common.Address]map[common.Hash]struct{}
}

func newAccessList() *accessList {
	return &accessList{
		addresses: make(map[common.Address]struct{}),
		slots:     make(map[common.Address]map[common.Hash]struct{}),
	}
}

func (al *accessList) AddAddress(addr common.Address) {
	al.addresses[addr] = struct{}{}
}

func (al *accessList) AddSlot(addr common.Address, slot common.Hash) {
	al.AddAddress(addr)
	if _, ok := al.slots[addr]; !ok {
		al.slots[addr] = make(map[common.Hash]struct{})
	}
	al.slots[addr][slot] = struct{}{}
}

func (al *accessList) ContainsAddress(addr common.Address) bool {
	_, ok := al.addresses[addr]
	return ok
}

func (al *accessList) Contains(addr common.Address, slot common.Hash) (bool, bool) {
	addrPresent := al.ContainsAddress(addr)
	slotPresent := false
	if addrPresent {
		if slots, ok := al.slots[addr]; ok {
			_, slotPresent = slots[slot]
		}
	}
	return addrPresent, slotPresent
}

func (c *ContractDB) GetStorageRoot(addr common.Address) common.Hash { return common.Hash{} }
func (c *ContractDB) GetSelfDestruction(addr common.Address) bool    { return false }
