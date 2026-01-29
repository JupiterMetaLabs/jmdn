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

	pbdid "gossipnode/DID/proto"
	pb "gossipnode/gETH/proto"
)

// ContractDB implements vm.StateDB by proxying account reads to a remote gETH node
// and storing contract state locally in PebbleDB.
// It is specifically designed for the SmartContract module to manage contract data.
type ContractDB struct {
	client    pb.ChainClient
	didClient pbdid.DIDServiceClient // DID Client for Balance/Nonce
	db        storage.KVStore

	// Caches and Dirty sets for Contract Storage
	storage      map[common.Address]map[common.Hash]common.Hash
	storageDirty map[common.Address]map[common.Hash]struct{}

	// Caches and Dirty sets for Contract Code
	code      map[common.Address][]byte
	codeDirty map[common.Address]struct{}

	// Nonce cache and dirty set (for local persistence)
	nonces     map[common.Address]uint64
	nonceDirty map[common.Address]struct{}

	// Balance cache (Ephemeral for transaction execution)
	balances map[common.Address]*uint256.Int

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
	PrefixNonce   = []byte("nonce:")
)

// NewContractDB creates a new database interface for Smart Contracts
func NewContractDB(client pb.ChainClient, didClient pbdid.DIDServiceClient, db storage.KVStore) *ContractDB {
	return &ContractDB{
		client:       client,
		didClient:    didClient,
		db:           db,
		storage:      make(map[common.Address]map[common.Hash]common.Hash),
		storageDirty: make(map[common.Address]map[common.Hash]struct{}),
		code:         make(map[common.Address][]byte),
		codeDirty:    make(map[common.Address]struct{}),
		nonces:       make(map[common.Address]uint64),
		nonceDirty:   make(map[common.Address]struct{}),
		balances:     make(map[common.Address]*uint256.Int),
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
	c.lock.Lock()
	defer c.lock.Unlock()

	bal := c.getBalanceInternal(addr)
	fmt.Printf("DEBUG: SubBalance called for %s, Amount: %s, Current: %s\n", addr.Hex(), amount.String(), bal.String())
	if bal.Lt(amount) {
		// In a real EVM, this should verify, but here we just set to 0 to avoid underflow if logic is weird
		// Or better, let it underflow if that's what EVM expects? No, EVM checks CanTransfer.
		// However, SubBalance is authoritative.
		// We trust the EVM checked CanTransfer.
		bal.Sub(bal, amount)
	} else {
		bal.Sub(bal, amount)
	}
	c.balances[addr] = bal
}

func (c *ContractDB) AddBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) {
	c.lock.Lock()
	defer c.lock.Unlock()

	bal := c.getBalanceInternal(addr)
	fmt.Printf("DEBUG: AddBalance called for %s, Amount: %s, Current: %s\n", addr.Hex(), amount.String(), bal.String())
	bal.Add(bal, amount)
	c.balances[addr] = bal
}

func (c *ContractDB) GetBalance(addr common.Address) *uint256.Int {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.getBalanceInternal(addr)
}

// getBalanceInternal must be called with lock held
func (c *ContractDB) getBalanceInternal(addr common.Address) *uint256.Int {
	// Check cache
	if bal, ok := c.balances[addr]; ok {
		// Return a copy to avoid mutation by caller?
		// uint256 pointers are mutable.
		// EVM expects a reference it can read, but SubBalance modifies it.
		// Safest is to return a new copy or the pointer in map.
		// Standard EVM practice: Return the pointer, but careful.
		// Here we make a copy to return, so the caller can't mutate our state without calling Sub/Add.
		return new(uint256.Int).Set(bal)
	}

	// Fetch from DID Service
	// We need to drop lock to call external service? No, because we need to update cache atomically?
	// But calling external service with lock held might deadlock or be slow.
	// For now, simple implementation.
	// We CANNOT call DID Client with lock held if GetBalanceInternal is strictly internal.
	// Refactor: Check cache (RLock), if miss, RUnlock, Fetch, Lock, Set.

	// BUT, upgrading lock is race-prone.
	// For now, holding lock is safer for correctness in single-thread-per-tx.

	if c.didClient == nil {
		return uint256.NewInt(0)
	}

	// Warn: External call with lock held.
	// Ideally we cache this at start of TX.
	// For now, we proceed.

	// We need to release lock to be safe from deadlocks if GetDID calls back? (Unlikely)
	// Actually, didClient is gRPC, so it's network IO. Blocking lock is bad but acceptable for prototype.

	// WARNING: We must NOT hold lock during network call if performance matters.
	// But `balances` map protection needs it.

	// Using a temporary variable to hold result
	var fetchedBal *uint256.Int

	// Use context.Background() or passed context? We don't have context here.
	req := &pbdid.GetDIDRequest{Did: addr.Hex()}
	resp, err := c.didClient.GetDID(context.Background(), req)

	if err != nil || resp.DidInfo == nil || resp.DidInfo.Balance == "" {
		fetchedBal = uint256.NewInt(0)
	} else {
		bigBal := new(big.Int)
		if val, ok := bigBal.SetString(resp.DidInfo.Balance, 0); ok {
			fetchedBal = new(uint256.Int)
			fetchedBal.SetFromBig(val)
		} else {
			fetchedBal = uint256.NewInt(0)
		}
	}

	// Cache it
	c.balances[addr] = new(uint256.Int).Set(fetchedBal)
	return fetchedBal
}

func (c *ContractDB) GetNonce(addr common.Address) uint64 {
	c.lock.RLock()
	// Check local cache first
	if nonce, ok := c.nonces[addr]; ok {
		c.lock.RUnlock()
		return nonce
	}
	c.lock.RUnlock()

	// Check persistent DB
	key := makeNonceKey(addr)
	val, err := c.db.Get(key)
	if err == nil && len(val) > 0 {
		return new(big.Int).SetBytes(val).Uint64()
	}

	// Fallback to DID Service for Nonce
	if c.didClient != nil {
		req := &pbdid.GetDIDRequest{Did: addr.Hex()}
		resp, err := c.didClient.GetDID(context.Background(), req)
		if err == nil && resp.DidInfo != nil && resp.DidInfo.Nonce != "" {
			nonceBig := new(big.Int)
			if val, ok := nonceBig.SetString(resp.DidInfo.Nonce, 0); ok {
				return val.Uint64()
			}
		}
	}

	// Default to 0 for new accounts
	return 0
}

func (c *ContractDB) SetNonce(addr common.Address, nonce uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.nonces[addr] = nonce
	c.nonceDirty[addr] = struct{}{}
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
	c.codeDirty = make(map[common.Address]struct{})

	// Commit Nonces
	for addr := range c.nonceDirty {
		nonce := c.nonces[addr]
		dbKey := makeNonceKey(addr)
		nonceBytes := new(big.Int).SetUint64(nonce).Bytes()
		if err := batch.Set(dbKey, nonceBytes); err != nil {
			return common.Hash{}, err
		}
	}
	// Clear nonce dirty map
	c.nonceDirty = make(map[common.Address]struct{})

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
	c.lock.Lock()
	defer c.lock.Unlock()
	if gas > c.refund {
		fmt.Printf("⚠️  SubRefund UNDERFLOW prevented! Current: %d, Sub: %d\n", c.refund, gas)
		c.refund = 0
		return
	}
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

func makeNonceKey(addr common.Address) []byte {
	return append(PrefixNonce, addr.Bytes()...)
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
