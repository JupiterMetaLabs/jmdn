package messaging

import (
	"errors"
	"testing"

	"gossipnode/config"
)

func parentAt(slot uint64) func() (uint64, error) {
	return func() (uint64, error) { return slot, nil }
}

func noParentRead(t *testing.T) func() (uint64, error) {
	return func() (uint64, error) {
		t.Helper()
		t.Fatal("the parent must only be read for a Slot-0 block")
		return 0, nil
	}
}

// ThebeSync's slot rule: slot = block number, whatever the Period.
func TestCheckSyncedSlot_SlotIsTheBlockNumber(t *testing.T) {
	for _, b := range []*config.ZKBlock{
		{BlockNumber: 0, Slot: 0},
		{BlockNumber: 20_000, Slot: 20_000},
		{BlockNumber: 20_000, Slot: 20_000, Period: 3},
	} {
		if err := CheckSyncedSlot(b, noParentRead(t)); err != nil {
			t.Fatalf("block %d slot %d period %d must sync: %v", b.BlockNumber, b.Slot, b.Period, err)
		}
	}
	for _, b := range []*config.ZKBlock{
		{BlockNumber: 20_000, Slot: 20_001},
		{BlockNumber: 20_000, Slot: 20_003, Period: 3}, // the old "slot jumps with period" shape
		{BlockNumber: 20_000, Slot: 19_999},
	} {
		if err := CheckSyncedSlot(b, noParentRead(t)); err == nil {
			t.Fatalf("block %d slot %d must be rejected", b.BlockNumber, b.Slot)
		}
	}
}

// History from before slots were stamped (Slot 0 at a real height) syncs, but
// only while the parent is also unslotted.
func TestCheckSyncedSlot_PreSlotHistory(t *testing.T) {
	legacy := &config.ZKBlock{BlockNumber: 7, Slot: 0}
	if err := CheckSyncedSlot(legacy, parentAt(0)); err != nil {
		t.Fatalf("a Slot-0 block under a Slot-0 parent must sync: %v", err)
	}
	if err := CheckSyncedSlot(legacy, parentAt(6)); err == nil {
		t.Fatal("a Slot-0 block after a slotted parent must be rejected")
	}
	if err := CheckSyncedSlot(legacy, func() (uint64, error) { return 0, errors.New("db down") }); err == nil {
		t.Fatal("an unreadable parent must fail closed")
	}
}
