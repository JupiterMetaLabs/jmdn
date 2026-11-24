package messaging

// D-49: AggSigStoreSlotsForTest and ResetAggSigStoreForTest, moved out of
// entropy_fallback_recovery.go. Both are test-only and every real caller
// already lives in a _test.go file in this same package, so no build tag is
// needed here — a _test.go file's symbols are already excluded from a normal
// build and only visible within this package's own test binary, which is
// exactly where every caller already is.

// AggSigStoreSlotsForTest returns the recorded slots. Test-only.
func AggSigStoreSlotsForTest() []uint64 {
	defaultAggSigStore.mu.Lock()
	defer defaultAggSigStore.mu.Unlock()
	out := make([]uint64, 0, len(defaultAggSigStore.sigs))
	for s := range defaultAggSigStore.sigs {
		out = append(out, s)
	}
	return out
}

// ResetAggSigStoreForTest clears the store. Test-only.
func ResetAggSigStoreForTest() {
	defaultAggSigStore.mu.Lock()
	defaultAggSigStore.sigs = make(map[uint64][]byte)
	defaultAggSigStore.mu.Unlock()
}
