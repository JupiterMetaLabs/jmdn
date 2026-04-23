package contractDB

import (
	"bytes"
	"context"
	"math/big"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	pbdid "gossipnode/DID/proto"
)

// stateObject represents a single Ethereum account's in-memory state.
// It tracks both the committed origin state (from DB) and the transient dirty state.
type stateObject struct {
	address  common.Address
	addrHash common.Hash // Keccak256(address)

	// originAccount is the account state at the beginning of the transaction.
	originAccount *AccountData

	// data is the current (possibly dirty) state.
	data *AccountData

	// Storage caches
	originStorage    map[common.Hash]common.Hash
	dirtyStorage     map[common.Hash]common.Hash
	dirtyStorageMeta map[common.Hash]StorageMetadata

	// Contract bytecode
	code []byte

	// Flags
	dirtyCode  bool
	dirtyNonce bool // set when SetNonce is called; lets CommitToDB skip no-op SaveNonce writes
	dirty      bool
	suicided   bool
	deleted    bool

	// Reference to the parent ContractDB for lazy DB loads.
	db *ContractDB
}

func newStateObject(db *ContractDB, addr common.Address, origin *AccountData) *stateObject {
	if origin == nil {
		origin = NewAccountData()
	}
	return &stateObject{
		address:          addr,
		addrHash:         crypto.Keccak256Hash(addr[:]),
		originAccount:    origin,
		data:             origin.Copy(),
		originStorage:    make(map[common.Hash]common.Hash),
		dirtyStorage:     make(map[common.Hash]common.Hash),
		dirtyStorageMeta: make(map[common.Hash]StorageMetadata),
		db:               db,
	}
}

// ============================================================================
// Balance
// ============================================================================

func (s *stateObject) getBalance() *uint256.Int {
	return new(uint256.Int).Set(s.data.Balance)
}

func (s *stateObject) setBalance(amount *uint256.Int) {
	s.data.Balance = new(uint256.Int).Set(amount)
	s.markDirty()
}

func (s *stateObject) addBalance(amount *uint256.Int) {
	s.setBalance(new(uint256.Int).Add(s.data.Balance, amount))
}

func (s *stateObject) subBalance(amount *uint256.Int) {
	s.setBalance(new(uint256.Int).Sub(s.data.Balance, amount))
}

// ============================================================================
// Nonce
// ============================================================================

func (s *stateObject) getNonce() uint64 { return s.data.Nonce }

func (s *stateObject) setNonce(nonce uint64) {
	s.data.Nonce = nonce
	s.dirtyNonce = true
	s.markDirty()
}

// ============================================================================
// Code
// ============================================================================

// getCode returns the contract bytecode, lazy-loading from the DB if needed.
func (s *stateObject) getCode() []byte {
	if s.code != nil {
		return s.code
	}
	val, err := s.db.repo.GetCode(context.Background(), s.address)
	if l := logger(); l != nil {
		l.Debug(context.Background(), "getCode lazy-load",
			ion.String("addr", s.address.Hex()),
			ion.Int("code_len", len(val)),
			ion.Bool("found", err == nil && len(val) > 0),
		)
	}
	if err == nil && len(val) > 0 {
		s.code = val
		s.data.CodeHash = crypto.Keccak256(val)
		return s.code
	}
	return nil
}

func (s *stateObject) setCode(code []byte) {
	if l := logger(); l != nil {
		l.Debug(context.Background(), "setCode",
			ion.String("addr", s.address.Hex()),
			ion.Int("code_len", len(code)),
		)
	}
	s.code = code
	s.data.CodeHash = crypto.Keccak256(code)
	s.dirtyCode = true
	s.markDirty()
}

func (s *stateObject) getCodeHash() []byte {
	if len(s.data.CodeHash) == 0 || bytes.Equal(s.data.CodeHash, emptyCodeHash) {
		if code := s.getCode(); code == nil {
			return emptyCodeHash
		}
	}
	return s.data.CodeHash
}

func (s *stateObject) getCodeSize() int { return len(s.getCode()) }

// ============================================================================
// Storage
// ============================================================================

func (s *stateObject) getState(key common.Hash) common.Hash {
	if value, ok := s.dirtyStorage[key]; ok {
		return value
	}
	if value, ok := s.originStorage[key]; ok {
		return value
	}
	value := s.loadStorage(key)
	s.originStorage[key] = value
	return value
}

func (s *stateObject) setState(key, value common.Hash) {
	s.dirtyStorage[key] = value
	s.dirtyStorageMeta[key] = StorageMetadata{
		ContractAddress:   s.address,
		StorageKey:        key,
		ValueHash:         crypto.Keccak256Hash(value.Bytes()),
		LastModifiedBlock: s.db.currentBlock,
		LastModifiedTx:    s.db.currentTxHash,
		UpdatedAt:         time.Now().UTC().Unix(),
	}
	s.markDirty()
}

func (s *stateObject) getCommittedState(key common.Hash) common.Hash {
	if value, ok := s.originStorage[key]; ok {
		return value
	}
	value := s.loadStorage(key)
	s.originStorage[key] = value
	return value
}

func (s *stateObject) loadStorage(key common.Hash) common.Hash {
	val, err := s.db.repo.GetStorage(context.Background(), s.address, key)
	if err != nil {
		return common.Hash{}
	}
	return val
}

// ============================================================================
// Flags
// ============================================================================

func (s *stateObject) markDirty() { s.dirty = true }
func (s *stateObject) isDirty() bool  { return s.dirty }

func (s *stateObject) isEmpty() bool {
	return s.data.Nonce == 0 &&
		s.data.Balance.Sign() == 0 &&
		bytes.Equal(s.getCodeHash(), emptyCodeHash)
}

func (s *stateObject) suicide() {
	s.suicided = true
	s.deleted = true
}

// ============================================================================
// Persistence helpers
// ============================================================================

func (s *stateObject) finalizeStorage() (toWrite map[common.Hash]common.Hash, toDelete []common.Hash, metaUpdates map[common.Hash]StorageMetadata) {
	toWrite = make(map[common.Hash]common.Hash)
	toDelete = make([]common.Hash, 0)
	metaUpdates = make(map[common.Hash]StorageMetadata)

	for key, value := range s.dirtyStorage {
		if value == (common.Hash{}) {
			toDelete = append(toDelete, key)
		} else {
			toWrite[key] = value
			if meta, ok := s.dirtyStorageMeta[key]; ok {
				metaUpdates[key] = meta
			}
		}
	}
	return
}

func (s *stateObject) commitStorage() {
	for key, value := range s.dirtyStorage {
		s.originStorage[key] = value
	}
	s.dirtyStorage = make(map[common.Hash]common.Hash)
	s.dirtyStorageMeta = make(map[common.Hash]StorageMetadata)
}

func (s *stateObject) commitCode() { s.dirtyCode = false }

func (s *stateObject) commitState() {
	s.commitStorage()
	s.commitCode()
	s.dirty = false
	s.dirtyNonce = false
	s.originAccount = s.data.Copy()
}

// ============================================================================
// Account loading helpers
// ============================================================================

// loadAccountFromDID fetches account balance and nonce from the JMDN DID service.
func loadAccountFromDID(didClient pbdid.DIDServiceClient, addr common.Address) *AccountData {
	if didClient == nil {
		return NewAccountData()
	}

	req := &pbdid.GetDIDRequest{Did: addr.Hex()}
	resp, err := didClient.GetDID(context.Background(), req)

	account := NewAccountData()
	if err != nil || resp.DidInfo == nil {
		return account
	}

	if resp.DidInfo.Balance != "" {
		bigBal := new(big.Int)
		if val, ok := bigBal.SetString(resp.DidInfo.Balance, 0); ok {
			account.Balance = new(uint256.Int)
			account.Balance.SetFromBig(val)
		}
	}

	if resp.DidInfo.Nonce != "" {
		nonceBig := new(big.Int)
		if val, ok := nonceBig.SetString(resp.DidInfo.Nonce, 0); ok {
			account.Nonce = val.Uint64()
		}
	}

	return account
}

// loadAccountFromLocalDB fetches any locally cached nonce from PebbleDB.
func loadAccountFromLocalDB(repo StateRepository, addr common.Address) *AccountData {
	account := NewAccountData()
	if nonce, err := repo.GetNonce(context.Background(), addr); err == nil && nonce > 0 {
		account.Nonce = nonce
	}
	return account
}
