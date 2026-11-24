package DB_OPs

// Tests for the read-side half of docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item
// 8's slot-restart fix. DB_OPs/backend's toBlockRecord (write side, tested in
// DB_OPs/backend/block_persist_slot_test.go) already persisted Slot/Period
// into a committed block's ExtraData — but blockRecordToZKBlock (this
// package, the read side used by GetZKBlockByNumber, which
// messaging.RecoverSlotStoreAtStartup's production getTip closure in main.go
// calls) never decoded those two keys back out. A restarted node reading its
// own tip block back would always see Slot=0/Period=0 regardless of what was
// actually persisted — silently defeating the write-side fix entirely.

import (
	"encoding/json"
	"testing"

	"gossipnode/DB_OPs/thebegateway"
)

// TestBlockRecordToZKBlock_RecoversSlotAndPeriod is the direct round-trip
// check, using float64 — not uint64 — because that is what every real
// caller actually gets: both DB_OPs/thebegateway/reader.go's scanBlock and
// the Redis cache decorator (DB_OPs/store/cache/block.go) populate
// BlockRecord.ExtraData via json.Unmarshal into a map[string]any, and JSON
// numbers decode to float64. A test using uint64 directly would pass even if
// the float64 case were broken, missing the actual production path.
func TestBlockRecordToZKBlock_RecoversSlotAndPeriod(t *testing.T) {
	rec := &thebegateway.BlockRecord{
		BlockNumber: 42,
		BlockHash:   "0x1",
		ParentHash:  "0x0",
		ExtraData: map[string]any{
			"slot":   float64(501),
			"period": float64(2),
		},
	}
	blk, err := blockRecordToZKBlock(rec)
	if err != nil {
		t.Fatalf("blockRecordToZKBlock: unexpected error: %v", err)
	}
	if blk.Slot != 501 {
		t.Fatalf("blk.Slot = %d, want 501", blk.Slot)
	}
	if blk.Period != 2 {
		t.Fatalf("blk.Period = %d, want 2", blk.Period)
	}
}

// TestBlockRecordToZKBlock_RecoversSlotAndPeriod_ThroughRealJSONRoundTrip goes
// one step further: marshal ExtraData to JSON and back first, exactly as the
// real SQL scan (extraJSON []byte -> json.Unmarshal) and the cache decorator
// both do, rather than hand-constructing a map with the "right" Go type.
func TestBlockRecordToZKBlock_RecoversSlotAndPeriod_ThroughRealJSONRoundTrip(t *testing.T) {
	written := map[string]any{"slot": uint64(777), "period": uint64(3)}
	raw, err := json.Marshal(written)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	rec := &thebegateway.BlockRecord{BlockNumber: 9, ExtraData: roundTripped}
	blk, err := blockRecordToZKBlock(rec)
	if err != nil {
		t.Fatalf("blockRecordToZKBlock: unexpected error: %v", err)
	}
	if blk.Slot != 777 {
		t.Fatalf("blk.Slot = %d, want 777 (JSON round-trip)", blk.Slot)
	}
	if blk.Period != 3 {
		t.Fatalf("blk.Period = %d, want 3 (JSON round-trip)", blk.Period)
	}
}

// TestBlockRecordToZKBlock_MissingSlotPeriodDefaultsToZero covers a record
// with no slot/period keys at all (e.g. a block committed before this
// persistence fix shipped) — must not panic or misbehave, just default to
// zero, which RecoverSlotStoreAtStartup's own caller-side check then treats
// as "cannot safely recover" for any BlockNumber > 0.
func TestBlockRecordToZKBlock_MissingSlotPeriodDefaultsToZero(t *testing.T) {
	rec := &thebegateway.BlockRecord{BlockNumber: 5, ExtraData: map[string]any{}}
	blk, err := blockRecordToZKBlock(rec)
	if err != nil {
		t.Fatalf("blockRecordToZKBlock: unexpected error: %v", err)
	}
	if blk.Slot != 0 || blk.Period != 0 {
		t.Fatalf("expected Slot=0/Period=0 when absent, got Slot=%d Period=%d", blk.Slot, blk.Period)
	}
}

func TestExtraDataUint64_HandlesAllObservedTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want uint64
	}{
		{"float64", float64(123), 123},
		{"uint64", uint64(456), 456},
		{"int64", int64(789), 789},
		{"int", int(10), 10},
		{"json.Number", json.Number("999"), 999},
		{"negative float64 clamps to 0", float64(-5), 0},
		{"unrecognized type defaults to 0", "not-a-number", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extraDataUint64(c.in); got != c.want {
				t.Fatalf("extraDataUint64(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
