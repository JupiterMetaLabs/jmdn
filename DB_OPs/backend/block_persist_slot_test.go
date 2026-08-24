package backend

// Tests for docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8's Tier 1 fix: a
// restarted node's SlotStore starts at 0 (slot_store.go's own header comment
// admits it "does NOT survive a process restart correctly") because
// toBlockRecord never carried Slot/Period into the persisted record for a
// startup path to read back. This closes that half of the gap; the other
// half (SlotStore.SeedFromCommittedTip) already exists and is tested in
// jmdn/messaging.

import (
	"testing"

	"gossipnode/config"
)

func TestToBlockRecord_PersistsSlotAndPeriod(t *testing.T) {
	b := &config.ZKBlock{
		BlockNumber: 42,
		Slot:        501,
		Period:      2,
	}
	rec := toBlockRecord(b)

	if rec.ExtraData == nil {
		t.Fatal("expected ExtraData to be populated with slot/period, got nil")
	}
	slot, ok := rec.ExtraData["slot"]
	if !ok {
		t.Fatal("expected ExtraData[\"slot\"] to be present")
	}
	if slot != uint64(501) {
		t.Fatalf("expected ExtraData[\"slot\"]=501, got %v", slot)
	}
	period, ok := rec.ExtraData["period"]
	if !ok {
		t.Fatal("expected ExtraData[\"period\"] to be present")
	}
	if period != uint64(2) {
		t.Fatalf("expected ExtraData[\"period\"]=2, got %v", period)
	}
}

// TestToBlockRecord_PersistsSlotEvenWhenZero matters because a block's own
// Slot/Period CAN legitimately be 0 (genesis, or the very first commit) -
// the old `if b.ExtraData != ""` guard pattern elsewhere in this file would
// have silently dropped a zero value if reused here. Slot/Period must always
// be written, never conditionally on being non-zero.
func TestToBlockRecord_PersistsSlotEvenWhenZero(t *testing.T) {
	b := &config.ZKBlock{BlockNumber: 0, Slot: 0, Period: 0}
	rec := toBlockRecord(b)

	if rec.ExtraData == nil {
		t.Fatal("expected ExtraData to be populated even for slot=0/period=0")
	}
	if slot, ok := rec.ExtraData["slot"]; !ok || slot != uint64(0) {
		t.Fatalf("expected ExtraData[\"slot\"]=0 present, got %v (present=%v)", slot, ok)
	}
	if period, ok := rec.ExtraData["period"]; !ok || period != uint64(0) {
		t.Fatalf("expected ExtraData[\"period\"]=0 present, got %v (present=%v)", period, ok)
	}
}

// TestToBlockRecord_SlotPeriodCoexistWithLegacyRawExtraData guards against
// the new keys clobbering (or being clobbered by) the pre-existing
// ExtraData["raw"] convention this function already had.
func TestToBlockRecord_SlotPeriodCoexistWithLegacyRawExtraData(t *testing.T) {
	b := &config.ZKBlock{BlockNumber: 1, Slot: 10, Period: 1, ExtraData: "legacy-payload"}
	rec := toBlockRecord(b)

	if raw, ok := rec.ExtraData["raw"]; !ok || raw != "legacy-payload" {
		t.Fatalf("expected legacy ExtraData[\"raw\"] to be preserved, got %v (present=%v)", raw, ok)
	}
	if slot, ok := rec.ExtraData["slot"]; !ok || slot != uint64(10) {
		t.Fatalf("expected ExtraData[\"slot\"]=10 to coexist with the legacy raw field, got %v (present=%v)", slot, ok)
	}
}
