package state

import (
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/trie/utils"
	"github.com/holiman/uint256"

	"gossipnode/config"
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
// It also serves as an in-memory state DB when dbClient is nil
type ImmuStateDB struct {
	// Database connection
	dbClient *config.PooledConnection

	accessList *accessList // Access list for EIP-2930

	// In-memory caches
	accounts          map[common.Address]*stateAccount
	stateObjects      map[common.Address]*stateObject
	stateObjectsDirty map[common.Address]struct{}

	// Transaction logs
	logs    []*types.Log
	logSize uint
	refund  uint64

	// Merkle trie root hash
	stateRoot common.Hash

	// Snapshots for reverting
	snapshots []*stateSnapshot

	// Concurrency control
	mutex sync.RWMutex

	// Deleted accounts
	suicided map[common.Address]bool

	// Transaction tracking for batch operations
	txOps   []*dbOperation
	txMutex sync.Mutex

	commit           map[common.Hash]struct{}
	transientStorage map[common.Address]map[common.Hash]common.Hash
	pointCache       *utils.PointCache
	selfdestruct6780 map[common.Address]struct{}
	hasselfdestruct  map[common.Address]bool
	selfdestruct     map[common.Address]struct{}

	// For stateless execution
	witness      *stateless.Witness
	witnessMutex sync.RWMutex
}

// Ensure ImmuStateDB implements vm.StateDB
var _ vm.StateDB = (*ImmuStateDB)(nil)

// NewImmuStateDB creates a new state database
// If client is nil, it acts as an in-memory database
func NewImmuStateDB(client *config.PooledConnection) *ImmuStateDB {
	var witness *stateless.Witness = nil
	return &ImmuStateDB{
		dbClient:          client,
		accounts:          make(map[common.Address]*stateAccount),
		stateObjects:      make(map[common.Address]*stateObject),
		stateObjectsDirty: make(map[common.Address]struct{}),
		logs:              make([]*types.Log, 0),
		snapshots:         make([]*stateSnapshot, 0),
		suicided:          make(map[common.Address]bool),
		txOps:             make([]*dbOperation, 0, dbBatchSize),
		accessList: &accessList{
			addresses: make(map[common.Address]struct{}),
			slots:     make(map[common.Address]map[common.Hash]struct{}),
		},
		commit:           make(map[common.Hash]struct{}),
		transientStorage: make(map[common.Address]map[common.Hash]common.Hash),
		pointCache:       utils.NewPointCache(4096),
		selfdestruct6780: make(map[common.Address]struct{}),
		hasselfdestruct:  make(map[common.Address]bool),
		selfdestruct:     make(map[common.Address]struct{}),
		witness:          witness,
		witnessMutex:     sync.RWMutex{},
	}
}

// NewInMemoryStateDB creates a state database with no redundant storage
func NewInMemoryStateDB() *ImmuStateDB {
	return NewImmuStateDB(nil)
}

// getDBKey formats a database key with appropriate prefix
func getDBKey(prefix string, addr common.Address, slot ...common.Hash) string {
	if len(slot) > 0 {
		return fmt.Sprintf("%s%s:%s", prefix, addr.Hex(), slot[0].Hex())
	}
	return fmt.Sprintf("%s%s", prefix, addr.Hex())
}

// Helper methods for internal state management

func (s *ImmuStateDB) getOrCreateStateObject(addr common.Address) *stateObject {
	if obj := s.getStateObject(addr); obj != nil {
		return obj
	}
	return s.createStateObject(addr)
}

func (s *ImmuStateDB) getStateObject(addr common.Address) *stateObject {
	if obj, ok := s.stateObjects[addr]; ok {
		if obj.deleted {
			return nil
		}
		return obj
	}
	// In a real implementation we might load from DB here if not found in memory
	// But mostly we assume if it's not in memory it might need loading via other methods
	return nil
}

func (s *ImmuStateDB) createStateObject(addr common.Address) *stateObject {
	account := stateAccount{
		BalanceU256:  uint256.NewInt(0),
		Storage:      make(map[common.Hash]common.Hash),
		StorageDirty: make(map[common.Hash]struct{}),
	}
	obj := &stateObject{
		address: addr,
		account: account,
		isDirty: true,
	}
	s.stateObjects[addr] = obj
	s.accounts[addr] = &obj.account
	return obj
}

// Exist checks whether an account exists at the given address
func (s *ImmuStateDB) Exist(addr common.Address) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.getStateObject(addr) != nil
}

// Empty checks whether an account is empty
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
