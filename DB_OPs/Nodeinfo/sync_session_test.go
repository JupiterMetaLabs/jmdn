package NodeInfo

import "testing"

// No active session: advances are not deferred and no tip is recorded.
func TestSyncSession_InactivePassthrough(t *testing.T) {
	if DeferLatestBlockAdvance(42) {
		t.Fatal("advance deferred with no active session")
	}
	if tip := PeekSyncSessionTip(); tip != 0 {
		t.Fatalf("tip recorded outside a session: %d", tip)
	}
}

// During a session, advances are deferred into a high-water mark; EndSyncSession
// returns it and the last session out clears it.
func TestSyncSession_DeferAndDrain(t *testing.T) {
	BeginSyncSession()
	if !DeferLatestBlockAdvance(100) {
		t.Fatal("advance not deferred during session")
	}
	DeferLatestBlockAdvance(90) // lower value must not lower the mark
	DeferLatestBlockAdvance(130)
	if tip := PeekSyncSessionTip(); tip != 130 {
		t.Fatalf("peek = %d, want 130", tip)
	}
	if tip := EndSyncSession(); tip != 130 {
		t.Fatalf("end = %d, want 130", tip)
	}
	if tip := PeekSyncSessionTip(); tip != 0 {
		t.Fatalf("tip not cleared after last session: %d", tip)
	}
	if DeferLatestBlockAdvance(5) {
		t.Fatal("advance still deferred after session end")
	}
}

// Overlapping sessions stack: the mark survives until the LAST session ends.
func TestSyncSession_Overlap(t *testing.T) {
	BeginSyncSession()
	BeginSyncSession()
	DeferLatestBlockAdvance(77)
	if tip := EndSyncSession(); tip != 77 {
		t.Fatalf("inner end = %d, want 77", tip)
	}
	// One session still active — deferral continues, mark retained.
	if !DeferLatestBlockAdvance(80) {
		t.Fatal("advance not deferred while outer session active")
	}
	if tip := EndSyncSession(); tip != 80 {
		t.Fatalf("outer end = %d, want 80", tip)
	}
	if PeekSyncSessionTip() != 0 {
		t.Fatal("tip not cleared after all sessions ended")
	}
}
