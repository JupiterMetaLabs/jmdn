package contractDB

import (
	"github.com/ethereum/go-ethereum/common"
)

// accessList tracks addresses and storage slots accessed during EVM transaction execution.
// Used for EIP-2929 and EIP-2930 compliance.
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

// AddAddress adds an address to the access list.
func (al *accessList) AddAddress(addr common.Address) {
	al.addresses[addr] = struct{}{}
}

// AddSlot adds an (address, slot) pair to the access list.
func (al *accessList) AddSlot(addr common.Address, slot common.Hash) {
	al.AddAddress(addr)
	if _, ok := al.slots[addr]; !ok {
		al.slots[addr] = make(map[common.Hash]struct{})
	}
	al.slots[addr][slot] = struct{}{}
}

// ContainsAddress reports whether addr is in the access list.
func (al *accessList) ContainsAddress(addr common.Address) bool {
	_, ok := al.addresses[addr]
	return ok
}

// Contains reports whether both addr and slot are in the access list.
// Returns (addressPresent, slotPresent).
func (al *accessList) Contains(addr common.Address, slot common.Hash) (bool, bool) {
	addrPresent := al.ContainsAddress(addr)
	if !addrPresent {
		return false, false
	}
	slots, ok := al.slots[addr]
	if !ok {
		return true, false
	}
	_, slotPresent := slots[slot]
	return true, slotPresent
}
