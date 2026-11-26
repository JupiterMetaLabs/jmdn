package MessagePassing

import (
	"encoding/json"
	"testing"

	"gossipnode/internal/roundlock"
)

func decodeAbstain(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("abstain reply is not JSON: %q: %v", raw, err)
	}
	if m["error"] != "node not synced; abstaining from consensus" {
		t.Fatalf("error text changed: %v", m["error"])
	}
	return m
}

// A node that signed period 3 at height 10 and is now not ready (restarted)
// still reports highest_period 3 when it abstains.
func TestAbstainReply_CarriesHighestPeriodFromResultLedger(t *testing.T) {
	l := withFreshRoundLedger(t)
	if ok, _, _, _ := l.TryLockFresh(roundlock.Round{Height: 10, Period: 3}, "A"); !ok {
		t.Fatal("setup lock refused")
	}
	m := decodeAbstain(t, abstainNotSyncedMessage(`{"block_hash":"0xab","block_number":10}`))
	if hp, ok := m["highest_period"].(float64); !ok || hp != 3 {
		t.Fatalf("highest_period = %v, want 3", m["highest_period"])
	}
}

// The vote ledger counts too (the incident: nodes held a period-1 ACCEPT vote).
func TestAbstainReply_CarriesHighestPeriodFromVoteLedger(t *testing.T) {
	withFreshRoundLedger(t)
	const h = 9_000_123 // unique height: roundlock.Votes is process-global
	if ok, _, _, _ := roundlock.Votes.TryLockFresh(roundlock.Round{Height: h, Period: 1}, "A"); !ok {
		t.Fatal("setup vote lock refused")
	}
	m := decodeAbstain(t, abstainNotSyncedMessage(`{"block_number":9000123}`))
	if hp, ok := m["highest_period"].(float64); !ok || hp != 1 {
		t.Fatalf("highest_period = %v, want 1", m["highest_period"])
	}
}

// Nothing signed at the height: no highest_period (absent, not 0, so the
// sequencer does not count a fake "0" report).
func TestAbstainReply_NoRecordNoField(t *testing.T) {
	withFreshRoundLedger(t)
	m := decodeAbstain(t, abstainNotSyncedMessage(`{"block_number":11}`))
	if _, ok := m["highest_period"]; ok {
		t.Fatalf("unexpected highest_period: %v", m)
	}
}

// Malformed or height-less requests get the plain abstain reply.
func TestAbstainReply_BadRequestIsPlainReply(t *testing.T) {
	withFreshRoundLedger(t)
	for _, req := range []string{"", "not json", `{"block_hash":"0xab"}`, `{"block_number":0}`} {
		m := decodeAbstain(t, abstainNotSyncedMessage(req))
		if _, ok := m["highest_period"]; ok {
			t.Fatalf("request %q: unexpected highest_period", req)
		}
	}
}
