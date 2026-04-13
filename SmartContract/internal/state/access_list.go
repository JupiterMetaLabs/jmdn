package state

import (
	"github.com/ethereum/go-ethereum/common"
)

// accessList tracks addresses and storage slots accessed during transaction execution.
// This is used for EIP-2929 and EIP-2930 compliance.
type accessList struct {
	addresses map[common.Address]struct{}
	slots     map[common.Address]map[common.Hash]struct{}
}

// newAccessList creates a new empty access list.
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

// AddSlot adds a storage slot to the access list.
func (al *accessList) AddSlot(addr common.Address, slot common.Hash) {
	al.AddAddress(addr)
	if _, ok := al.slots[addr]; !ok {
		al.slots[addr] = make(map[common.Hash]struct{})
	}
	al.slots[addr][slot] = struct{}{}
}

// ContainsAddress checks if an address is in the access list.
func (al *accessList) ContainsAddress(addr common.Address) bool {
	_, ok := al.addresses[addr]
	return ok
}

// Contains checks if both an address and storage slot are in the access  list.
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
