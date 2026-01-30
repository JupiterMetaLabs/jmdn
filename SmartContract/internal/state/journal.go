package state

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// journalEntry is an interface for reversible state changes.
// Each modification to the state creates a journal entry that can be reverted.
type journalEntry interface {
	// revert undoes the change made to the state
	revert(*ContractDB)

	// dirtied returns the address that was modified (optional, can return nil)
	dirtied() *common.Address
}

// journal tracks all state changes for snapshot/revert functionality.
// Changes are logged sequentially and can be reverted in reverse order.
type journal struct {
	entries []journalEntry
	dirties map[common.Address]int // Maps address to first dirty entry index
}

// newJournal creates a new empty journal.
func newJournal() *journal {
	return &journal{
		entries: make([]journalEntry, 0),
		dirties: make(map[common.Address]int),
	}
}

// append adds a new journal entry to the log.
func (j *journal) append(entry journalEntry) {
	j.entries = append(j.entries, entry)
	if addr := entry.dirtied(); addr != nil {
		if _, exist := j.dirties[*addr]; !exist {
			j.dirties[*addr] = len(j.entries) - 1
		}
	}
}

// revert undoes all changes from the specified snapshot to the current state.
func (j *journal) revert(db *ContractDB, snapshot int) {
	for i := len(j.entries) - 1; i >= snapshot; i-- {
		j.entries[i].revert(db)
	}
	j.entries = j.entries[:snapshot]

	// Clean up dirties map
	for addr, idx := range j.dirties {
		if idx >= snapshot {
			delete(j.dirties, addr)
		}
	}
}

// length returns the current journal length (used for snapshot IDs).
func (j *journal) length() int {
	return len(j.entries)
}

// dirty marks an address as having been modified at the given journal index.
func (j *journal) dirty(addr common.Address) {
	if _, exist := j.dirties[addr]; !exist {
		j.dirties[addr] = len(j.entries)
	}
}

// ============================================================================
// Journal Entry Implementations
// ============================================================================

// createObjectChange is logged when a new state object is created.
type createObjectChange struct {
	account *common.Address
}

func (ch createObjectChange) revert(s *ContractDB) {
	delete(s.stateObjects, *ch.account)
}

func (ch createObjectChange) dirtied() *common.Address {
	return ch.account
}

// ============================================================================

// balanceChange tracks a balance modification.
type balanceChange struct {
	account *common.Address
	prev    *uint256.Int
}

func (ch balanceChange) revert(s *ContractDB) {
	s.getStateObject(*ch.account).setBalance(ch.prev)
}

func (ch balanceChange) dirtied() *common.Address {
	return ch.account
}

// ============================================================================

// nonceChange tracks a nonce modification.
type nonceChange struct {
	account *common.Address
	prev    uint64
}

func (ch nonceChange) revert(s *ContractDB) {
	s.getStateObject(*ch.account).setNonce(ch.prev)
}

func (ch nonceChange) dirtied() *common.Address {
	return ch.account
}

// ============================================================================

// codeChange tracks contract code modification.
type codeChange struct {
	account  *common.Address
	prevcode []byte
	prevhash []byte
}

func (ch codeChange) revert(s *ContractDB) {
	obj := s.getStateObject(*ch.account)
	obj.setCode(ch.prevcode)
	obj.data.CodeHash = ch.prevhash
}

func (ch codeChange) dirtied() *common.Address {
	return ch.account
}

// ============================================================================

// storageChange tracks a storage slot modification.
type storageChange struct {
	account  *common.Address
	key      common.Hash
	prevalue common.Hash
}

func (ch storageChange) revert(s *ContractDB) {
	s.getStateObject(*ch.account).setState(ch.key, ch.prevalue)
}

func (ch storageChange) dirtied() *common.Address {
	return ch.account
}

// ============================================================================

// suicideChange tracks the suicide/selfdestruct flag.
type suicideChange struct {
	account     *common.Address
	prev        bool // previous suicide state
	prevbalance *uint256.Int
}

func (ch suicideChange) revert(s *ContractDB) {
	obj := s.getStateObject(*ch.account)
	obj.suicided = ch.prev
	obj.setBalance(ch.prevbalance)
}

func (ch suicideChange) dirtied() *common.Address {
	return ch.account
}

// ============================================================================

// refundChange tracks gas refund modifications.
type refundChange struct {
	prev uint64
}

func (ch refundChange) revert(s *ContractDB) {
	s.refund = ch.prev
}

func (ch refundChange) dirtied() *common.Address {
	return nil
}

// ============================================================================

// addLogChange tracks log additions.
type addLogChange struct {
	txhash common.Hash
}

func (ch addLogChange) revert(s *ContractDB) {
	logs := s.logs
	if len(logs) == 1 {
		s.logs = nil
	} else {
		s.logs = logs[:len(logs)-1]
	}
}

func (ch addLogChange) dirtied() *common.Address {
	return nil
}

// ============================================================================

// accessListAddAccountChange tracks access list account additions.
type accessListAddAccountChange struct {
	address *common.Address
}

func (ch accessListAddAccountChange) revert(s *ContractDB) {
	delete(s.accessList.addresses, *ch.address)
}

func (ch accessListAddAccountChange) dirtied() *common.Address {
	return nil
}

// ============================================================================

// accessListAddSlotChange tracks access list slot additions.
type accessListAddSlotChange struct {
	address *common.Address
	slot    *common.Hash
}

func (ch accessListAddSlotChange) revert(s *ContractDB) {
	if slots, ok := s.accessList.slots[*ch.address]; ok {
		delete(slots, *ch.slot)
		if len(slots) == 0 {
			delete(s.accessList.slots, *ch.address)
		}
	}
}

func (ch accessListAddSlotChange) dirtied() *common.Address {
	return nil
}
