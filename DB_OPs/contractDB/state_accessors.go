package contractDB

import (
	"context"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/holiman/uint256"
)

// ============================================================================
// Internal state object access
// ============================================================================

// getStateObject returns the state object for addr (lazy-loading from DB).
// Returns nil only if addr has never been touched and doesn't exist in DB.
func (c *ContractDB) getStateObject(addr common.Address) *stateObject {
	c.lock.RLock()
	if obj, ok := c.stateObjects[addr]; ok {
		c.lock.RUnlock()
		return obj
	}
	c.lock.RUnlock()

	c.lock.Lock()
	defer c.lock.Unlock()

	// Double-check after acquiring write lock.
	if obj, ok := c.stateObjects[addr]; ok {
		return obj
	}

	// Load from DID service, merge with locally cached nonce.
	accountData := loadAccountFromDID(c.didClient, addr)
	localData := loadAccountFromLocalDB(c.repo, addr)
	if localData.Nonce > accountData.Nonce {
		accountData.Nonce = localData.Nonce
	}

	obj := newStateObject(c, addr, accountData)
	c.stateObjects[addr] = obj
	return obj
}

// getOrNewStateObject returns the state object for addr, creating it if absent.
func (c *ContractDB) getOrNewStateObject(addr common.Address) *stateObject {
	if obj := c.getStateObject(addr); obj != nil {
		return obj
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	if obj, ok := c.stateObjects[addr]; ok {
		return obj
	}

	obj := newStateObject(c, addr, NewAccountData())
	c.stateObjects[addr] = obj
	c.journal.append(createObjectChange{account: &addr})
	return obj
}

// ============================================================================
// Account lifecycle
// ============================================================================

// CreateAccount creates a new account for addr (or marks it dirty if it exists).
func (c *ContractDB) CreateAccount(addr common.Address) {
	obj := c.getOrNewStateObject(addr)
	if obj != nil {
		obj.markDirty()
	}
}

// CreateContract is an alias for CreateAccount used during contract deployment.
func (c *ContractDB) CreateContract(addr common.Address) {
	c.CreateAccount(addr)
}

// ============================================================================
// Balance
// ============================================================================

func (c *ContractDB) SubBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) uint256.Int {
	obj := c.getStateObject(addr)
	if obj == nil {
		return uint256.Int{}
	}
	prev := obj.getBalance()
	c.journal.append(balanceChange{account: &addr, prev: prev})
	obj.subBalance(amount)
	if l := logger(); l != nil {
		l.Debug(context.Background(), "SubBalance",
			ion.String("addr", addr.Hex()),
			ion.String("amount", amount.String()),
			ion.String("new_balance", obj.getBalance().String()),
		)
	}
	return *obj.getBalance()
}

func (c *ContractDB) AddBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) uint256.Int {
	obj := c.getStateObject(addr)
	if obj == nil {
		return uint256.Int{}
	}
	prev := obj.getBalance()
	c.journal.append(balanceChange{account: &addr, prev: prev})
	obj.addBalance(amount)
	if l := logger(); l != nil {
		l.Debug(context.Background(), "AddBalance",
			ion.String("addr", addr.Hex()),
			ion.String("amount", amount.String()),
			ion.String("new_balance", obj.getBalance().String()),
		)
	}
	return *obj.getBalance()
}

func (c *ContractDB) GetBalance(addr common.Address) *uint256.Int {
	obj := c.getStateObject(addr)
	if obj == nil {
		return uint256.NewInt(0)
	}
	return obj.getBalance()
}

// ============================================================================
// Nonce
// ============================================================================

func (c *ContractDB) GetNonce(addr common.Address) uint64 {
	obj := c.getStateObject(addr)
	if obj == nil {
		return 0
	}
	return obj.getNonce()
}

func (c *ContractDB) SetNonce(addr common.Address, nonce uint64, reason tracing.NonceChangeReason) {
	obj := c.getOrNewStateObject(addr)
	if obj == nil {
		return
	}
	c.journal.append(nonceChange{account: &addr, prev: obj.getNonce()})
	obj.setNonce(nonce)
}

// ============================================================================
// Code
// ============================================================================

func (c *ContractDB) GetCodeHash(addr common.Address) common.Hash {
	obj := c.getStateObject(addr)
	if obj == nil {
		return common.Hash{}
	}
	return common.BytesToHash(obj.getCodeHash())
}

func (c *ContractDB) GetCode(addr common.Address) []byte {
	obj := c.getStateObject(addr)
	if obj == nil {
		return nil
	}
	return obj.getCode()
}

func (c *ContractDB) SetCode(addr common.Address, code []byte, reason tracing.CodeChangeReason) []byte {
	obj := c.getOrNewStateObject(addr)
	if obj == nil {
		return nil
	}
	c.journal.append(codeChange{
		account:  &addr,
		prevcode: obj.getCode(),
		prevhash: obj.getCodeHash(),
	})
	obj.setCode(code)
	return code
}

func (c *ContractDB) GetCodeSize(addr common.Address) int {
	obj := c.getStateObject(addr)
	if obj == nil {
		return 0
	}
	return obj.getCodeSize()
}

// ============================================================================
// Storage
// ============================================================================

func (c *ContractDB) GetState(addr common.Address, key common.Hash) common.Hash {
	obj := c.getStateObject(addr)
	if obj == nil {
		return common.Hash{}
	}
	return obj.getState(key)
}

func (c *ContractDB) SetState(addr common.Address, key common.Hash, value common.Hash) common.Hash {
	obj := c.getOrNewStateObject(addr)
	if obj == nil {
		return common.Hash{}
	}
	c.journal.append(storageChange{account: &addr, key: key, prevalue: obj.getState(key)})
	obj.setState(key, value)
	return value
}

func (c *ContractDB) GetCommittedState(addr common.Address, key common.Hash) common.Hash {
	obj := c.getStateObject(addr)
	if obj == nil {
		return common.Hash{}
	}
	return obj.getCommittedState(key)
}

// ============================================================================
// Snapshot / revert
// ============================================================================

func (c *ContractDB) Snapshot() int {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.journal.length()
}

// RevertToSnapshot undoes all state changes since snapshot.
// NOTE: Must NOT hold c.lock here — journal revert callbacks call getStateObject
// which acquires the same RWMutex (Go's RWMutex is not re-entrant).
func (c *ContractDB) RevertToSnapshot(snapshot int) {
	c.journal.revert(c, snapshot)
}

// ============================================================================
// Account existence
// ============================================================================

func (c *ContractDB) Exist(addr common.Address) bool {
	obj := c.getStateObject(addr)
	return obj != nil && !obj.isEmpty()
}

func (c *ContractDB) Empty(addr common.Address) bool {
	obj := c.getStateObject(addr)
	return obj == nil || obj.isEmpty()
}

// ============================================================================
// Self-destruct
// ============================================================================

func (c *ContractDB) Suicide(addr common.Address) bool {
	obj := c.getStateObject(addr)
	if obj == nil {
		return false
	}
	c.journal.append(suicideChange{
		account:     &addr,
		prev:        obj.suicided,
		prevbalance: obj.getBalance(),
	})
	obj.suicide()
	return true
}

func (c *ContractDB) SelfDestruct(addr common.Address) {
	c.Suicide(addr)
}

func (c *ContractDB) HasSuicided(addr common.Address) bool {
	obj := c.getStateObject(addr)
	return obj != nil && obj.suicided
}

func (c *ContractDB) HasSelfDestructed(addr common.Address) bool {
	return c.HasSuicided(addr)
}

func (c *ContractDB) Selfdestruct6780(addr common.Address) {
	c.Suicide(addr)
}
