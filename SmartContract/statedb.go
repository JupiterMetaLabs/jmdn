package SmartContract

import (
    "encoding/json"
    "fmt"
    "math/big"
    "sync"
    "time"

    "github.com/ethereum/go-ethereum/common"
    "github.com/ethereum/go-ethereum/core/types"
    "github.com/ethereum/go-ethereum/crypto"
    "github.com/ethereum/go-ethereum/trie"
    "github.com/holiman/uint256"
    "github.com/rs/zerolog/log"
    
    "gossipnode/DB_OPs"
    "gossipnode/config"
    "gossipnode/helper"
)

const (
    // Database key prefixes for different data types
    prefixBalance   = "balance:"
    prefixNonce     = "nonce:"
    prefixCode      = "code:"
    prefixCodeHash  = "codehash:"
    prefixStorage   = "storage:"
    prefixStateRoot = "stateroot:"
    
    // Transaction batch size for database operations
    dbBatchSize = 100
)

// ImmuStateDB implements vm.StateDB using ImmuDB for persistent storage
type ImmuStateDB struct {
    // Database connection
    dbClient      *config.ImmuClient
    
    // In-memory caches
    accounts      map[common.Address]*stateAccount
    stateObjects  map[common.Address]*stateObject
    stateObjectsDirty map[common.Address]struct{}
    
    // Transaction logs
    logs          []*types.Log
    logSize       uint
    
    // Refund counter
    refund        uint64
    
    // Merkle trie root hash
    stateRoot     common.Hash
    
    // Snapshots for reverting
    snapshots     []*stateSnapshot
    
    // Concurrency control
    mutex         sync.RWMutex
    
    // Deleted accounts
    suicided      map[common.Address]bool
    
    // Transaction tracking for batch operations
    txOps         []*dbOperation
    txMutex       sync.Mutex
}

// stateAccount represents an Ethereum account
type stateAccount struct {
    Balance     *big.Int
    BalanceU256 *uint256.Int // Cached uint256 representation
    Nonce       uint64
    Code        []byte
    CodeHash    common.Hash
    Storage     map[common.Hash]common.Hash
    StorageDirty map[common.Hash]struct{}
}

// stateObject represents an Ethereum account with processing state
type stateObject struct {
    address  common.Address
    account  stateAccount
    isDirty  bool
    deleted  bool
}

// stateSnapshot represents a point-in-time snapshot of the state
type stateSnapshot struct {
    id        int
    accounts  map[common.Address]stateAccount
    suicided  map[common.Address]bool
}

// dbOperation represents a pending database operation
type dbOperation struct {
    key     string
    value   []byte
    isDelete bool
}

// NewImmuStateDB creates a new state database with ImmuDB persistence
func NewImmuStateDB(client *config.ImmuClient) *ImmuStateDB {
    return &ImmuStateDB{
        dbClient:     client,
        accounts:     make(map[common.Address]*stateAccount),
        stateObjects: make(map[common.Address]*stateObject),
        stateObjectsDirty: make(map[common.Address]struct{}),
        logs:         make([]*types.Log, 0),
        snapshots:    make([]*stateSnapshot, 0),
        suicided:     make(map[common.Address]bool),
        txOps:        make([]*dbOperation, 0, dbBatchSize),
    }
}

// DB Access Helper Methods
// =======================

// getDBKey formats a database key with appropriate prefix
func getDBKey(prefix string, addr common.Address, slot ...common.Hash) string {
    if len(slot) > 0 {
        return fmt.Sprintf("%s%s:%s", prefix, addr.Hex(), slot[0].Hex())
    }
    return fmt.Sprintf("%s%s", prefix, addr.Hex())
}

// queueDBOperation adds an operation to the transaction batch
func (s *ImmuStateDB) queueDBOperation(key string, value []byte, isDelete bool) {
    s.txMutex.Lock()
    defer s.txMutex.Unlock()
    
    op := &dbOperation{
        key:      key,
        value:    value,
        isDelete: isDelete,
    }
    
    s.txOps = append(s.txOps, op)
    
    // If we've reached the batch size, commit the batch
    if len(s.txOps) >= dbBatchSize {
        s.commitBatch()
    }
}

// commitBatch writes all queued operations to the database
func (s *ImmuStateDB) commitBatch() error {
    if len(s.txOps) == 0 {
        return nil
    }
    
    // Start a database transaction
    err := DB_OPs.Transaction(s.dbClient, func(tx *config.ImmuTransaction) error {
        for _, op := range s.txOps {
            if op.isDelete {
                // Handle deletion if needed
                // Note: ImmuDB might not support actual deletion
                continue
            }
            
            if err := DB_OPs.Set(tx, op.key, op.value); err != nil {
                return err
            }
        }
        return nil
    })
    
    if err != nil {
        log.Error().Err(err).Int("op_count", len(s.txOps)).Msg("Failed to commit state batch to database")
        return err
    }
    
    // Clear the batch
    s.txOps = s.txOps[:0]
    return nil
}

// Account Methods
// ==============

// CreateAccount creates a new account
func (s *ImmuStateDB) CreateAccount(addr common.Address) {
    s.mutex.Lock()
    defer s.mutex.Unlock()
    
    // Delete any existing account
    if obj := s.getStateObject(addr); obj != nil {
        // Mark as deleted if it existed
        s.suicided[addr] = true
    }
    
    // Create a new account
    newAccount := &stateAccount{
        Balance:     new(big.Int),
        BalanceU256: uint256.NewInt(0),
        Nonce:       0,
        Code:        nil,
        CodeHash:    crypto.Keccak256Hash(nil),
        Storage:     make(map[common.Hash]common.Hash),
        StorageDirty: make(map[common.Hash]struct{}),
    }
    
    s.accounts[addr] = newAccount
    obj := s.getOrCreateStateObject(addr)
    obj.isDirty = true
    s.stateObjectsDirty[addr] = struct{}{}
    
    delete(s.suicided, addr)
}

// getStateObject retrieves a state object for the given address
func (s *ImmuStateDB) getStateObject(addr common.Address) *stateObject {
    // First check in-memory cache
    if obj, ok := s.stateObjects[addr]; ok {
        if obj.deleted {
            return nil
        }
        return obj
    }
    
    // Not in cache, try to load from database
    if err := s.loadAccount(addr); err != nil {
        log.Warn().Err(err).Str("address", addr.Hex()).Msg("Failed to load account from database")
        return nil
    }
    
    // Check if it was loaded successfully
    if obj, ok := s.stateObjects[addr]; ok {
        return obj
    }
    
    return nil
}

// getOrCreateStateObject gets an existing state object or creates a new one
func (s *ImmuStateDB) getOrCreateStateObject(addr common.Address) *stateObject {
    if obj := s.getStateObject(addr); obj != nil {
        return obj
    }
    
    // Create a new one
    account := &stateAccount{
        Balance:     new(big.Int),
        BalanceU256: uint256.NewInt(0),
        Nonce:       0,
        Code:        nil,
        CodeHash:    crypto.Keccak256Hash(nil),
        Storage:     make(map[common.Hash]common.Hash),
        StorageDirty: make(map[common.Hash]struct{}),
    }
    
    s.accounts[addr] = account
    
    stateObject := &stateObject{
        address:  addr,
        account:  *account,
        isDirty:  true,
        deleted:  false,
    }
    
    s.stateObjects[addr] = stateObject
    s.stateObjectsDirty[addr] = struct{}{}
    
    return stateObject
}

// loadAccount loads an account from the database
func (s *ImmuStateDB) loadAccount(addr common.Address) error {
    // Skip if the account is already in memory
    if _, ok := s.stateObjects[addr]; ok {
        return nil
    }
    
    // Check if the account exists in the database
    balanceKey := getDBKey(prefixBalance, addr)
    balanceData, err := DB_OPs.Read(s.dbClient, balanceKey)
    
    // If the account doesn't exist in the database, return without error
    if err != nil {
        return nil // Not finding an account is not an error
    }
    
    // Create a new account
    account := &stateAccount{
        Balance:     new(big.Int),
        BalanceU256: uint256.NewInt(0),
        Storage:     make(map[common.Hash]common.Hash),
        StorageDirty: make(map[common.Hash]struct{}),
    }
    
    // Load the balance
    if balanceData != nil {
        account.Balance.SetBytes(balanceData)
        account.BalanceU256, _ = uint256.FromBig(account.Balance)
    }
    
    // Load the nonce
    nonceKey := getDBKey(prefixNonce, addr)
    nonceData, err := DB_OPs.Read(s.dbClient, nonceKey)
    if err == nil && nonceData != nil {
        var nonce uint64
        if err := json.Unmarshal(nonceData, &nonce); err == nil {
            account.Nonce = nonce
        }
    }
    
    // Load the code hash
    codeHashKey := getDBKey(prefixCodeHash, addr)
    codeHashData, err := DB_OPs.Read(s.dbClient, codeHashKey)
    if err == nil && codeHashData != nil {
        copy(account.CodeHash[:], codeHashData)
        
        // If we have a code hash, load the code
        codeKey := getDBKey(prefixCode, addr)
        codeData, err := DB_OPs.Read(s.dbClient, codeKey)
        if err == nil && codeData != nil {
            account.Code = codeData
        }
    } else {
        account.CodeHash = crypto.Keccak256Hash(nil)
    }
    
    // Create a state object and add it to the cache
    s.accounts[addr] = account
    stateObject := &stateObject{
        address: addr,
        account: *account,
        isDirty: false,
        deleted: false,
    }
    s.stateObjects[addr] = stateObject
    
    return nil
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

// Balance Methods
// ==============

// GetBalance retrieves the balance of the given address
func (s *ImmuStateDB) GetBalance(addr common.Address) *uint256.Int {
    s.mutex.RLock()
    defer s.mutex.RUnlock()
    
    stateObject := s.getOrCreateStateObject(addr)
    return stateObject.account.BalanceU256
}

// AddBalance adds amount to the account associated with addr
func (s *ImmuStateDB) AddBalance(addr common.Address, amount *uint256.Int, reason string) {
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
func (s *ImmuStateDB) SubBalance(addr common.Address, amount *uint256.Int, reason string) {
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
            Balance:     new(big.Int).Set(account.Balance),
            BalanceU256: uint256.NewInt(0).Set(account.BalanceU256),
            Nonce:       account.Nonce,
            Code:        append([]byte{}, account.Code...),
            CodeHash:    account.CodeHash,
            Storage:     storageCopy,
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

// Commit writes all changes to the database
func (s *ImmuStateDB) Commit(deleteEmptyObjects bool) (common.Hash, error) {
    // Prepare state for committing
    s.Finalise(deleteEmptyObjects)
    
    s.mutex.Lock()
    defer s.mutex.Unlock()
    
    // Reset transaction batch
    s.txOps = s.txOps[:0]
    
    // Track start time for logging
    startTime := time.Now()
    
    // Persist all dirty state objects
    for addr := range s.stateObjectsDirty {
        obj := s.stateObjects[addr]
        
        // Skip deleted accounts
        if obj.deleted {
            // If we wanted to delete, we would need to handle that here
            // But ImmuDB is append-only, so we might need a different approach
            continue
        }
        
        // Save balance
        balanceKey := getDBKey(prefixBalance, addr)
        s.queueDBOperation(balanceKey, obj.account.Balance.Bytes(), false)
        
        // Save nonce
        nonceKey := getDBKey(prefixNonce, addr)
        nonceData, _ := json.Marshal(obj.account.Nonce)
        s.queueDBOperation(nonceKey, nonceData, false)
        
        // Save code and code hash
        if len(obj.account.Code) > 0 {
            codeKey := getDBKey(prefixCode, addr)
            s.queueDBOperation(codeKey, obj.account.Code, false)
            
            codeHashKey := getDBKey(prefixCodeHash, addr)
            s.queueDBOperation(codeHashKey, obj.account.CodeHash[:], false)
        }
        
        // Save dirty storage slots
        for slot := range obj.account.StorageDirty {
            value := obj.account.Storage[slot]
            storageKey := getDBKey(prefixStorage, addr, slot)
            s.queueDBOperation(storageKey, value[:], false)
        }
    }
    
    // Commit any remaining operations
    if err := s.commitBatch(); err != nil {
        return common.Hash{}, fmt.Errorf("failed to commit state: %w", err)
    }
    
    // Generate a state root hash (in production this should be a real merkle root)
    stateRoot := s.generateStateRoot()
    s.stateRoot = stateRoot
    
    // Store the state root for recovery
    stateRootKey := fmt.Sprintf("%slatest", prefixStateRoot)
    if err := DB_OPs.Create(s.dbClient, stateRootKey, stateRoot[:]); err != nil {
        log.Error().Err(err).Msg("Failed to store state root hash")
    }
    
    // Clear the dirty flags
    s.stateObjectsDirty = make(map[common.Address]struct{})
    for _, obj := range s.stateObjects {
        obj.isDirty = false
        obj.account.StorageDirty = make(map[common.Hash]struct{})
    }
    
    // Clear suicided accounts
    s.suicided = make(map[common.Address]bool)
    
    // Clear logs
    s.logs = s.logs[:0]
    s.logSize = 0
    
    // Clear snapshots
    s.snapshots = s.snapshots[:0]
    
    // Log performance metrics
    log.Info().
        Dur("duration", time.Since(startTime)).
        Str("root", stateRoot.Hex()).
        Int("dirty_accounts", len(s.stateObjectsDirty)).
        Msg("State committed to database")
    
    return stateRoot, nil
}

// generateStateRoot creates a hash representing the current state
func (s *ImmuStateDB) generateStateRoot() common.Hash {
    // In production, this should construct a proper Merkle trie
    // For now, we'll use a simple approach
    h := crypto.NewKeccakState()
    
    // Sort addresses for deterministic output
    addrs := make([]common.Address, 0, len(s.accounts))
    for addr := range s.accounts {
        addrs = append(addrs, addr)
    }
    
    // Add each account to the hash
    for _, addr := range addrs {
        account := s.accounts[addr]
        
        // Hash the account data
        h.Write(addr[:])
        h.Write(account.Balance.Bytes())
        h.Write(helper.Uint64ToBytes(account.Nonce))
        h.Write(account.CodeHash[:])
        
        // Add storage
        for key, value := range account.Storage {
            h.Write(key[:])
            h.Write(value[:])
        }
    }
    
    var root common.Hash
    h.Read(root[:])
    return root
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
    stateRootKey := fmt.Sprintf("%slatest", prefixStateRoot)
    rootData, err := DB_OPs.Read(s.dbClient, stateRootKey)
    if err == nil && len(rootData) == common.HashLength {
        copy(s.stateRoot[:], rootData)
        log.Info().Str("root", s.stateRoot.Hex()).Msg("Loaded state root from database")
    }
    
    return nil
}

// GetTrie returns a read-only trie representing the state (placeholder implementation)
func (s *ImmuStateDB) GetTrie() *trie.Trie {
    // This would return a trie representation of the state
    // For now, we return nil as we don't have a full trie implementation
    return nil
}

// CodeIterator returns an iterator for all contracts in the state
func (s *ImmuStateDB) CodeIterator() *CodeIterator {
    return &CodeIterator{
        stateDB: s,
        addrs:   make([]common.Address, 0),
        index:   0,
    }
}

// CodeIterator implements an iterator over all contracts
type CodeIterator struct {
    stateDB *ImmuStateDB
    addrs   []common.Address
    index   int
}

// Next advances the iterator to the next contract
func (it *CodeIterator) Next() bool {
    // Lazy initialization of addresses
    if it.index == 0 && len(it.addrs) == 0 {
        it.stateDB.mutex.RLock()
        for addr, obj := range it.stateDB.stateObjects {
            if len(obj.account.Code) > 0 {
                it.addrs = append(it.addrs, addr)
            }
        }
        it.stateDB.mutex.RUnlock()
    }
    
    it.index++
    return it.index <= len(it.addrs)
}

// Address returns the address of the current contract
func (it *CodeIterator) Address() common.Address {
    if it.index <= 0 || it.index > len(it.addrs) {
        return common.Address{}
    }
    return it.addrs[it.index-1]
}

// Code returns the code of the current contract
func (it *CodeIterator) Code() []byte {
    if it.index <= 0 || it.index > len(it.addrs) {
        return nil
    }
    return it.stateDB.GetCode(it.addrs[it.index-1])
}