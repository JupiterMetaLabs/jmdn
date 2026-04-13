package state

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/holiman/uint256"
)

// ============================================================================
// State Object Access
// ============================================================================

// getStateObject retrieves a state object for the given address.
// Returns nil if the object doesn't exist. Does NOT create a new object.
func (c *ContractDB) getStateObject(addr common.Address) *stateObject {
	c.lock.RLock()
	if obj, ok := c.stateObjects[addr]; ok {
		c.lock.RUnlock()
		return obj
	}
	c.lock.RUnlock()

	// Lazy load from DB
	c.lock.Lock()
	defer c.lock.Unlock()

	// Double-check after acquiring write lock
	if obj, ok := c.stateObjects[addr]; ok {
		return obj
	}

	// Load account data from DID service
	accountData := loadAccountFromDID(c.didClient, addr)

	// Merge with any locally cached nonce
	localData := loadAccountFromLocalDB(c.repo, addr)
	if localData.Nonce > accountData.Nonce {
		accountData.Nonce = localData.Nonce
	}

	// Create state object
	obj := newStateObject(c, addr, accountData)
	c.stateObjects[addr] = obj

	return obj
}

// getOrNewStateObject retrieves or creates a state object for the given address.
// This is used when we need to ensure an object exists (e.g., during CreateAccount).
func (c *ContractDB) getOrNewStateObject(addr common.Address) *stateObject {
	obj := c.getStateObject(addr)
	if obj != nil {
		return obj
	}

	// Create new empty state object
	c.lock.Lock()
	defer c.lock.Unlock()

	// Double-check after acquiring write lock
	if obj, ok := c.stateObjects[addr]; ok {
		return obj
	}

	obj = newStateObject(c, addr, NewAccountData())
	c.stateObjects[addr] = obj

	// Log the creation in journal
	c.journal.append(createObjectChange{account: &addr})

	return obj
}

// ============================================================================
// Account State Methods
// ============================================================================

// CreateAccount explicitly creates a new account.
func (c *ContractDB) CreateAccount(addr common.Address) {
	obj := c.getOrNewStateObject(addr)
	if obj != nil {
		obj.markDirty()
	}
}

// CreateContract creates a new contract account.
func (c *ContractDB) CreateContract(addr common.Address) {
	c.CreateAccount(addr)
}

// SubBalance subtracts amount from the account's balance.
func (c *ContractDB) SubBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) {
	obj := c.getStateObject(addr)
	if obj == nil {
		return
	}

	// Record previous balance in journal
	prev := obj.getBalance()
	c.journal.append(balanceChange{
		account: &addr,
		prev:    prev,
	})

	// Update balance
	obj.subBalance(amount)

	fmt.Printf("DEBUG: SubBalance called for %s, Amount: %s, New Balance: %s\n",
		addr.Hex(), amount.String(), obj.getBalance().String())
}

// AddBalance adds amount to the account's balance.
func (c *ContractDB) AddBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) {
	obj := c.getStateObject(addr)
	if obj == nil {
		return
	}

	// Record previous balance in journal
	prev := obj.getBalance()
	c.journal.append(balanceChange{
		account: &addr,
		prev:    prev,
	})

	// Update balance
	obj.addBalance(amount)

	fmt.Printf("DEBUG: AddBalance called for %s, Amount: %s, New Balance: %s\n",
		addr.Hex(), amount.String(), obj.getBalance().String())
}

// GetBalance returns the balance of the given account.
func (c *ContractDB) GetBalance(addr common.Address) *uint256.Int {
	obj := c.getStateObject(addr)
	if obj == nil {
		return uint256.NewInt(0)
	}
	return obj.getBalance()
}

// GetNonce returns the nonce of the given account.
func (c *ContractDB) GetNonce(addr common.Address) uint64 {
	obj := c.getStateObject(addr)
	if obj == nil {
		return 0
	}
	return obj.getNonce()
}

// SetNonce sets the nonce of the given account.
func (c *ContractDB) SetNonce(addr common.Address, nonce uint64) {
	obj := c.getOrNewStateObject(addr)
	if obj == nil {
		return
	}

	// Record previous nonce in journal
	c.journal.append(nonceChange{
		account: &addr,
		prev:    obj.getNonce(),
	})

	// Update nonce
	obj.setNonce(nonce)
}

// GetCodeHash returns the code hash of the given account.
func (c *ContractDB) GetCodeHash(addr common.Address) common.Hash {
	obj := c.getStateObject(addr)
	if obj == nil {
		return common.Hash{}
	}
	return common.BytesToHash(obj.getCodeHash())
}

// GetCode returns the code of the given account.
func (c *ContractDB) GetCode(addr common.Address) []byte {
	obj := c.getStateObject(addr)
	if obj == nil {
		return nil
	}
	return obj.getCode()
}

// SetCode sets the code of the given account.
func (c *ContractDB) SetCode(addr common.Address, code []byte) {
	obj := c.getOrNewStateObject(addr)
	if obj == nil {
		return
	}

	// Record previous code in journal
	c.journal.append(codeChange{
		account:  &addr,
		prevcode: obj.getCode(),
		prevhash: obj.getCodeHash(),
	})

	// Update code
	obj.setCode(code)
}

// GetCodeSize returns the size of the code of the given account.
func (c *ContractDB) GetCodeSize(addr common.Address) int {
	obj := c.getStateObject(addr)
	if obj == nil {
		return 0
	}
	return obj.getCodeSize()
}

// ============================================================================
// Contract Storage Methods
// ============================================================================

// GetState returns the value of a storage slot.
func (c *ContractDB) GetState(addr common.Address, key common.Hash) common.Hash {
	obj := c.getStateObject(addr)
	if obj == nil {
		return common.Hash{}
	}
	return obj.getState(key)
}

// SetState sets the value of a storage slot.
func (c *ContractDB) SetState(addr common.Address, key common.Hash, value common.Hash) {
	obj := c.getOrNewStateObject(addr)
	if obj == nil {
		return
	}

	// Record previous value in journal
	c.journal.append(storageChange{
		account:  &addr,
		key:      key,
		prevalue: obj.getState(key),
	})

	// Update storage
	obj.setState(key, value)
}

// GetCommittedState returns the committed value of a storage slot.
func (c *ContractDB) GetCommittedState(addr common.Address, key common.Hash) common.Hash {
	obj := c.getStateObject(addr)
	if obj == nil {
		return common.Hash{}
	}
	return obj.getCommittedState(key)
}

// ============================================================================
// Snapshot and Revert
// ============================================================================

// Snapshot returns an identifier for the current state.
func (c *ContractDB) Snapshot() int {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.journal.length()
}

// RevertToSnapshot reverts all state changes made since the given snapshot.
func (c *ContractDB) RevertToSnapshot(snapshot int) {
	// Do NOT hold c.lock here.
	// Journal revert callbacks call getStateObject, which acquires the same RWMutex.
	// Holding the write lock would deadlock (RWMutex is not re-entrant).
	c.journal.revert(c, snapshot)
}

// ============================================================================
// Account Existence
// ============================================================================

// Exist returns whether the given account exists.
func (c *ContractDB) Exist(addr common.Address) bool {
	obj := c.getStateObject(addr)
	if obj == nil {
		return false
	}
	return !obj.isEmpty()
}

// Empty returns whether the given account is empty.
func (c *ContractDB) Empty(addr common.Address) bool {
	obj := c.getStateObject(addr)
	if obj == nil {
		return true
	}
	return obj.isEmpty()
}

// ============================================================================
// Suicide/SelfDestruct
// ============================================================================

// Suicide marks the given account for deletion.
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

// SelfDestruct is an alias for Suicide.
func (c *ContractDB) SelfDestruct(addr common.Address) {
	c.Suicide(addr)
}

// HasSuicided returns whether the given account has been marked for deletion.
func (c *ContractDB) HasSuicided(addr common.Address) bool {
	obj := c.getStateObject(addr)
	if obj == nil {
		return false
	}
	return obj.suicided
}

// HasSelfDestructed is an alias for HasSuicided.
func (c *ContractDB) HasSelfDestructed(addr common.Address) bool {
	return c.HasSuicided(addr)
}

// Selfdestruct6780 implements EIP-6780 selfdestruct.
func (c *ContractDB) Selfdestruct6780(addr common.Address) {
	c.Suicide(addr)
}
