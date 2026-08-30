# Fallback Fold: Count-Based Collection + Slot Deadline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the fixed-slot-range fallback fold (`[K, K+B)`, which halts permanently on a single timeout) with a count-based collection (`B` = number of committed-block signers, timeouts skipped) bounded by a separate slot deadline, and fix the pre-existing bug where `maybeFinaliseCompletedEpochs` tries to resolve the fallback seed at the exact instant the collection window has zero data.

**Architecture:** `B` stays a *count* of independent committed-block signers (security requirement — enough contributors that no single actor controls the fold). A new, separate constant, `FallbackFoldMaxSlotOffset`, is a *slot deadline* (liveness requirement — the point past which there isn't enough VDF runway left regardless of outcome). Finalisation splits into two phases: at the reveal cutoff, decide mixed-vs-fallback only; a fallback outcome enters a pending state that is re-checked on every subsequent committed block until either `B` signers are collected (seal) or the deadline slot passes (fail closed, permanently, no retry).

**Tech Stack:** Go, `github.com/JupiterMetaLabs/avc/randao` (pure fold logic), `gossipnode/messaging` (per-node collection state), existing `zerolog` logging, standard `testing` package (no new test framework).

**Spec:** `/home/claude/avc-docs/AVC-Architecture-End-to-End.md` §4.2a, §7.2, §10 decision 11 (device path: `~/Block/JMDT-Knowledgebase/AVC-Architecture-End-to-End.md`). This plan amends decision 11's fixed `[K,K+B)` window to the count+deadline design; the amendment itself is not yet written into that doc — Task 6 includes updating it.

## Global Constraints

- `FallbackFoldBufferB = 5` stays a **count**, never a slot number — this plan exists specifically to keep that distinction from collapsing back into the fixed-window bug.
- Fail closed always: no path may return `randao.Fallback()`'s offline-precomputable formula, a partial fold, or a fold with duplicate/out-of-order slots.
- `AggCertEnabled` / `JMDN_AVC_AGG_CERT` (default off) is unchanged by this plan — B1's certificate-carrying gate stays exactly as it is.
- No git commit in either repo unless the user explicitly asks in that turn (standing session rule).
- After each task's local verification, push the changed files to the device via `device_stage_files` → edit happens in `/root/work/{jmdn,avc}` → `device_commit_files` back to `~/Block/{jmdn,avc}`, matching how every other file in this session has been synced.
- Both repos must independently `go build ./...` and `go test ./...` clean after every task — this plan touches a package (`avc/randao`) that a sibling repo (`jmdn`) imports, so a break in one is invisible until the other is built.

---

## File Structure

| File | Responsibility after this plan |
|---|---|
| `avc/randao/fallback_aggsig.go` | Pure fold math: `FallbackCollectionBounds` (start/deadline, no `b`), `FallbackFromCommittedSigners` (fold exactly `b` signers, gaps allowed). `FallbackWindow`/`FallbackFromAggSigs` (the old fixed-width pair) are deleted — nothing outside this file and its own tests will still call them once Task 4 lands. |
| `avc/randao/fallback_aggsig_test.go` | Tests for the two functions above; the old fixed-window tests are rewritten, not left dangling. |
| `jmdn/messaging/entropy_fallback_window.go` | Per-node signer store (unchanged) plus the new three-outcome `FallbackSeedForEpoch(epoch, currentSlot)`; owns `FallbackFoldMaxSlotOffset` and the two new sentinel errors. |
| `jmdn/messaging/entropy_fallback_window_test.go` | Tests for the three-outcome behaviour: ready / not-yet-ready / deadline-exceeded. |
| `jmdn/messaging/entropy_aggsig.go` | `CertificateForBlockAssembly` migrated to `FallbackCollectionBounds`'s deadline instead of `FallbackWindow`'s fixed end. |
| `jmdn/messaging/entropy_aggsig_test.go` | One updated test for the migrated bound (existing cert-window tests, if any, adjusted to the new deadline semantics). |
| `jmdn/messaging/entropy_finalise.go` | Split into `decideEpoch` (mixed-vs-fallback decision at cutoff) and `resolvePendingFallbacks` (per-block retry loop for pending epochs) — replaces the old single-shot `finaliseEpoch`/`resolveFallbackSeed`. |
| `jmdn/messaging/entropy_finalise_test.go` | Rewritten for the new function signatures and the pending-state lifecycle. |
| `verify-m4.sh` | One new check (Task 6): a fallback epoch that starts pending with zero signers and later collects `B` must actually seal — the exact scenario that was previously impossible. |

---

## Task 1: `avc/randao` — `FallbackCollectionBounds` (pure, replaces `FallbackWindow`)

**Files:**
- Modify: `avc/randao/fallback_aggsig.go`
- Test: `avc/randao/fallback_aggsig_test.go`

**Interfaces:**
- Produces: `FallbackCollectionBounds(epoch, n, k, maxOffset uint64) (start, deadline uint64, err error)`

- [ ] **Step 1: Write the failing tests**

```go
func TestFallbackCollectionBounds_HappyPath(t *testing.T) {
	start, deadline, err := FallbackCollectionBounds(9, 50, 3, 7)
	if err != nil {
		t.Fatalf("FallbackCollectionBounds: %v", err)
	}
	if start != 9*50+3 {
		t.Fatalf("start = %d, want %d", start, 9*50+3)
	}
	if deadline != start+7 {
		t.Fatalf("deadline = %d, want %d", deadline, start+7)
	}
}

func TestFallbackCollectionBounds_RejectsBadParams(t *testing.T) {
	cases := []struct {
		name             string
		n, k, maxOffset  uint64
	}{
		{"zero n", 0, 3, 7},
		{"k >= n", 50, 50, 7},
		{"zero maxOffset", 50, 3, 0},
		{"k+maxOffset >= n", 50, 45, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := FallbackCollectionBounds(0, c.n, c.k, c.maxOffset); err == nil {
				t.Fatalf("%s: want an error, got nil", c.name)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd avc && go test ./randao/ -run TestFallbackCollectionBounds -v`
Expected: FAIL — `undefined: FallbackCollectionBounds`

- [ ] **Step 3: Implement `FallbackCollectionBounds`, delete `FallbackWindow`**

Add to `avc/randao/fallback_aggsig.go` (replacing the existing `FallbackWindow` function body and its doc comment — same file, same section):

```go
// FallbackCollectionBounds returns the half-open slot range [start, deadline)
// in which the fallback fold may collect committed-block signers for epoch.
//
// This is deliberately NOT a b-wide window. b committed signers are collected
// somewhere inside [start, deadline), skipping any slot whose round timed
// out — a run of timeouts simply reaches further into the range without
// weakening the fold, because the fold's security comes from the COUNT of
// signers, not from which exact slots they landed on. deadline is the hard
// stop: past it there is not enough guaranteed VDF runway left for the epoch
// (Architecture §10 decision 11's liveness ceiling), so collection must give
// up and the epoch fails closed if fewer than b signers were found by then.
//
// Renamed and widened from the earlier FallbackWindow(epoch,n,k,b), which
// required every slot in [k,k+b) to have a signer — one timeout inside that
// range made the fold permanently uncomputable for the epoch. This function
// takes maxOffset (a slot count, owned by the caller same as n/k) instead of
// b, because b no longer determines where the range ends.
func FallbackCollectionBounds(epoch, n, k, maxOffset uint64) (start, deadline uint64, err error) {
	switch {
	case n == 0:
		return 0, 0, fmt.Errorf("%w: n (slots per epoch) must be > 0", ErrBadWindowParams)
	case k >= n:
		return 0, 0, fmt.Errorf("%w: k=%d must be < n=%d", ErrBadWindowParams, k, n)
	case maxOffset == 0:
		return 0, 0, fmt.Errorf("%w: maxOffset (collection deadline, in slots past k) must be >= 1", ErrBadWindowParams)
	case k+maxOffset >= n:
		return 0, 0, fmt.Errorf("%w: k+maxOffset=%d must be < n=%d so collection closes before the epoch ends",
			ErrBadWindowParams, k+maxOffset, n)
	}
	start = epoch*n + k
	return start, start + maxOffset, nil
}
```

Delete the old `FallbackWindow` function entirely (it is fully superseded — nothing may call it after Task 4).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd avc && go test ./randao/ -run TestFallbackCollectionBounds -v`
Expected: PASS

- [ ] **Step 5: Delete the now-obsolete `FallbackWindow` tests**

Remove `TestFallbackWindow*` test functions from `avc/randao/fallback_aggsig_test.go` (they test a function that no longer exists). Do not leave them commented out.

- [ ] **Step 6: Full package test + build**

Run: `cd avc && go build ./... && go test ./randao/ -v`
Expected: build succeeds; the only failures, if any, are in `TestFallbackWindow*`-adjacent tests you have not yet touched in Task 2 (fine — Task 2 removes those too). If anything else fails, stop and fix before proceeding.

- [ ] **Step 7: Commit**

```bash
cd avc && git add randao/fallback_aggsig.go randao/fallback_aggsig_test.go
git commit -m "randao: replace fixed-width FallbackWindow with FallbackCollectionBounds"
```

---

## Task 2: `avc/randao` — `FallbackFromCommittedSigners` (replaces `FallbackFromAggSigs`)

**Files:**
- Modify: `avc/randao/fallback_aggsig.go`
- Test: `avc/randao/fallback_aggsig_test.go`

**Interfaces:**
- Consumes: `AggSig{Slot uint64; Sig []byte}` (existing type, unchanged), `aggSigTerm`, `writeField`, `writeU64` (existing unexported helpers, unchanged), `AggSigLen`, `ErrFallbackWindowIncomplete`, `ErrAggSigSlotOutsideWindow`, `ErrDuplicateAggSigSlot`, `ErrBadAggSig` (existing error vars, unchanged).
- Produces: `FallbackFromCommittedSigners(chainID, epoch, start, deadline, b uint64, sigs []AggSig) (Seed, error)`

- [ ] **Step 1: Write the failing tests**

```go
func TestFallbackFromCommittedSigners_ToleratesGaps(t *testing.T) {
	// The exact worked example from the design discussion: signers land at
	// slots 3,4,6,8,9 (5 and 7 timed out) inside [3, 10).
	sigs := []AggSig{
		{Slot: 3, Sig: fixedAggSig(0x01)},
		{Slot: 4, Sig: fixedAggSig(0x02)},
		{Slot: 6, Sig: fixedAggSig(0x03)},
		{Slot: 8, Sig: fixedAggSig(0x04)},
		{Slot: 9, Sig: fixedAggSig(0x05)},
	}
	seed, err := FallbackFromCommittedSigners(7000700, 0, 3, 10, 5, sigs)
	if err != nil {
		t.Fatalf("FallbackFromCommittedSigners: %v", err)
	}
	if seed == (Seed{}) {
		t.Fatal("returned the zero seed")
	}
}

func TestFallbackFromCommittedSigners_WrongCountRejected(t *testing.T) {
	sigs := []AggSig{{Slot: 3, Sig: fixedAggSig(0x01)}, {Slot: 4, Sig: fixedAggSig(0x02)}}
	if _, err := FallbackFromCommittedSigners(7000700, 0, 3, 10, 5, sigs); !errors.Is(err, ErrFallbackWindowIncomplete) {
		t.Fatalf("got %v, want ErrFallbackWindowIncomplete", err)
	}
}

func TestFallbackFromCommittedSigners_OutOfOrderOrDuplicateRejected(t *testing.T) {
	dup := []AggSig{
		{Slot: 4, Sig: fixedAggSig(0x01)},
		{Slot: 3, Sig: fixedAggSig(0x02)}, // not strictly increasing
	}
	if _, err := FallbackFromCommittedSigners(7000700, 0, 3, 10, 2, dup); !errors.Is(err, ErrDuplicateAggSigSlot) {
		t.Fatalf("got %v, want ErrDuplicateAggSigSlot", err)
	}
}

func TestFallbackFromCommittedSigners_SlotOutsideBoundsRejected(t *testing.T) {
	sigs := []AggSig{{Slot: 2, Sig: fixedAggSig(0x01)}} // before start=3
	if _, err := FallbackFromCommittedSigners(7000700, 0, 3, 10, 1, sigs); !errors.Is(err, ErrAggSigSlotOutsideWindow) {
		t.Fatalf("got %v, want ErrAggSigSlotOutsideWindow", err)
	}
}

func TestFallbackFromCommittedSigners_DeterministicRegardlessOfWhichSlotsGapped(t *testing.T) {
	// Two different gap patterns, same 5 signer VALUES at different slots,
	// must NOT produce the same seed (the slot is bound into the hash) —
	// this guards the "deliberate deviation" property this file already
	// documents for the old fold, carried over to the new one.
	a := []AggSig{{3, fixedAggSig(0x01)}, {4, fixedAggSig(0x02)}, {6, fixedAggSig(0x03)}, {8, fixedAggSig(0x04)}, {9, fixedAggSig(0x05)}}
	b := []AggSig{{3, fixedAggSig(0x01)}, {4, fixedAggSig(0x02)}, {5, fixedAggSig(0x03)}, {6, fixedAggSig(0x04)}, {7, fixedAggSig(0x05)}}
	seedA, err := FallbackFromCommittedSigners(7000700, 0, 3, 10, 5, a)
	if err != nil {
		t.Fatalf("seedA: %v", err)
	}
	seedB, err := FallbackFromCommittedSigners(7000700, 0, 3, 10, 5, b)
	if err != nil {
		t.Fatalf("seedB: %v", err)
	}
	if seedA == seedB {
		t.Fatal("two different slot layouts produced the same seed — the slot binding is not doing its job")
	}
}

// fixedAggSig returns an AggSigLen-byte value filled with b, for test fixtures.
func fixedAggSig(b byte) []byte {
	out := make([]byte, AggSigLen)
	for i := range out {
		out[i] = b
	}
	return out
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd avc && go test ./randao/ -run TestFallbackFromCommittedSigners -v`
Expected: FAIL — `undefined: FallbackFromCommittedSigners`

- [ ] **Step 3: Implement `FallbackFromCommittedSigners`, delete `FallbackFromAggSigs`**

Add to `avc/randao/fallback_aggsig.go`, replacing `FallbackFromAggSigs`:

```go
// FallbackFromCommittedSigners derives epoch's fallback seed from exactly b
// committed-block signers collected somewhere inside [start, deadline).
//
// This is FallbackFromAggSigs amended to tolerate timed-out slots: the old
// function required every slot in a b-wide range to contribute, so a single
// timeout inside the range made the fold permanently impossible. Here the
// range can be wider than b (bounded by deadline), and collection succeeds
// as soon as b signers are found — timeouts just widen the range consumed,
// they do not block the fold.
//
// Fails closed unless: len(sigs) == b exactly; every slot lies in
// [start, deadline); slots are strictly increasing (rules out both
// duplicates and out-of-order input in one check, since the caller always
// hands this function slots it read out of its own store in ascending
// order — a violation here means a caller bug, not attacker input, and
// must be loud). Each signer's slot is still bound into its hash term
// (aggSigTerm, unchanged) for the same reason the original fold documented:
// XOR is self-inverse, so binding the slot is what stops two identical
// aggregates at different slots from being interchangeable or cancelling.
func FallbackFromCommittedSigners(chainID, epoch, start, deadline, b uint64, sigs []AggSig) (Seed, error) {
	if uint64(len(sigs)) != b {
		return Seed{}, fmt.Errorf("%w: got %d signers, want exactly %d", ErrFallbackWindowIncomplete, len(sigs), b)
	}

	var acc Seed
	var prevSlot uint64
	for i, s := range sigs {
		if s.Slot < start || s.Slot >= deadline {
			return Seed{}, fmt.Errorf("%w: slot %d not in [%d,%d)", ErrAggSigSlotOutsideWindow, s.Slot, start, deadline)
		}
		if i > 0 && s.Slot <= prevSlot {
			return Seed{}, fmt.Errorf("%w: slot %d is not strictly after %d — signers must be strictly increasing with no duplicates",
				ErrDuplicateAggSigSlot, s.Slot, prevSlot)
		}
		if len(s.Sig) != AggSigLen {
			return Seed{}, fmt.Errorf("%w: slot %d has a %d-byte signature, want %d", ErrBadAggSig, s.Slot, len(s.Sig), AggSigLen)
		}
		term := aggSigTerm(chainID, epoch, s.Slot, s.Sig)
		for j := range acc {
			acc[j] ^= term[j]
		}
		prevSlot = s.Slot
	}

	h := sha256.New()
	writeField(h, []byte(domainFallbackAggSig))
	writeU64(h, chainID)
	writeU64(h, epoch)
	writeField(h, acc[:])
	var out Seed
	copy(out[:], h.Sum(nil))
	return out, nil
}
```

Delete `FallbackFromAggSigs` entirely.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd avc && go test ./randao/ -run TestFallbackFromCommittedSigners -v`
Expected: PASS (all 5 subtests)

- [ ] **Step 5: Delete the now-obsolete `FallbackFromAggSigs` tests**

Remove `TestFallbackFromAggSigs*` from `avc/randao/fallback_aggsig_test.go`.

- [ ] **Step 6: Full package test + build**

Run: `cd avc && go build ./... && go test ./... -v`
Expected: everything in `avc` passes. This is the last task confined to `avc` alone — `jmdn` still references the deleted functions and will not build again until Task 4. That is expected and fine at this checkpoint.

- [ ] **Step 7: Commit**

```bash
cd avc && git add randao/fallback_aggsig.go randao/fallback_aggsig_test.go
git commit -m "randao: replace fixed-width FallbackFromAggSigs with FallbackFromCommittedSigners"
```

- [ ] **Step 8: Sync to device**

Stage `avc/randao/fallback_aggsig.go` and `avc/randao/fallback_aggsig_test.go` via `device_stage_files`, confirm the diff, then `device_commit_files` into `~/Block/avc/randao/`.

---

## Task 3: `jmdn/messaging/entropy_fallback_window.go` — three-outcome `FallbackSeedForEpoch`

**Files:**
- Modify: `jmdn/messaging/entropy_fallback_window.go`
- Test: `jmdn/messaging/entropy_fallback_window_test.go`

**Interfaces:**
- Consumes: `randao.FallbackCollectionBounds`, `randao.FallbackFromCommittedSigners`, `randao.AggSig` (Task 1–2), existing `defaultAggSigStore`, `N`, `RevealCutoffK` (unchanged).
- Produces: `FallbackFoldMaxSlotOffset` (new const), `ErrFallbackNotYetReady`, `ErrFallbackDeadlineExceeded` (new sentinel errors), `FallbackSeedForEpoch(epoch, currentSlot uint64) (randao.Seed, error)` (signature changed — was `FallbackSeedForEpoch(epoch uint64)`).

- [ ] **Step 1: Write the failing tests**

```go
func TestFallbackSeedForEpoch_NotYetReadyBeforeDeadline(t *testing.T) {
	resetAggSigStore(t)
	start, _, err := randaoFallbackCollectionBoundsForTest(0)
	if err != nil {
		t.Fatalf("bounds: %v", err)
	}
	// Only 2 of the required 5 signers recorded; well inside the deadline.
	mustRecordAggSig(t, start, fixedSig(0x01))
	mustRecordAggSig(t, start+1, fixedSig(0x02))

	_, err = FallbackSeedForEpoch(0, start+1)
	if !errors.Is(err, ErrFallbackNotYetReady) {
		t.Fatalf("got %v, want ErrFallbackNotYetReady", err)
	}
}

func TestFallbackSeedForEpoch_ReadyOnceFiveCollectedWithGaps(t *testing.T) {
	resetAggSigStore(t)
	start, _, err := randaoFallbackCollectionBoundsForTest(0)
	if err != nil {
		t.Fatalf("bounds: %v", err)
	}
	// Slots start, start+1, start+3, start+5, start+6 committed;
	// start+2 and start+4 behave as if they timed out (nothing recorded).
	offsets := []uint64{0, 1, 3, 5, 6}
	for i, off := range offsets {
		mustRecordAggSig(t, start+off, fixedSig(byte(i+1)))
	}
	seed, err := FallbackSeedForEpoch(0, start+offsets[len(offsets)-1])
	if err != nil {
		t.Fatalf("FallbackSeedForEpoch: %v", err)
	}
	if seed == (randao.Seed{}) {
		t.Fatal("returned the zero seed")
	}
}

func TestFallbackSeedForEpoch_DeadlineExceededWithTooFewSigners(t *testing.T) {
	resetAggSigStore(t)
	start, deadline, err := randaoFallbackCollectionBoundsForTest(0)
	if err != nil {
		t.Fatalf("bounds: %v", err)
	}
	mustRecordAggSig(t, start, fixedSig(0x01)) // only 1 of 5
	_, err = FallbackSeedForEpoch(0, deadline)  // at the deadline slot itself
	if !errors.Is(err, ErrFallbackDeadlineExceeded) {
		t.Fatalf("got %v, want ErrFallbackDeadlineExceeded", err)
	}
}

// randaoFallbackCollectionBoundsForTest exposes the real bounds this package
// uses, so tests never hardcode N/K/MaxOffset independently of production.
func randaoFallbackCollectionBoundsForTest(epoch uint64) (start, deadline uint64, err error) {
	return randao.FallbackCollectionBounds(epoch, N, RevealCutoffK, FallbackFoldMaxSlotOffset)
}

func mustRecordAggSig(t *testing.T, slot uint64, sig []byte) {
	t.Helper()
	if err := RecordAggSigForFallback(slot, sig); err != nil {
		t.Fatalf("RecordAggSigForFallback(%d): %v", slot, err)
	}
}

func fixedSig(b byte) []byte {
	out := make([]byte, randao.AggSigLen)
	for i := range out {
		out[i] = b
	}
	return out
}

func resetAggSigStore(t *testing.T) {
	t.Helper()
	saved := defaultAggSigStore
	defaultAggSigStore = &aggSigStore{sigs: make(map[uint64][]byte)}
	t.Cleanup(func() { defaultAggSigStore = saved })
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd jmdn && go test ./messaging/ -run TestFallbackSeedForEpoch -v`
Expected: FAIL — build error (`FallbackFoldMaxSlotOffset` undefined, `FallbackSeedForEpoch` wrong arg count). Note: the whole `messaging` package will not build yet because `entropy_aggsig.go` (Task 4) and `entropy_finalise.go` (Task 5) still call the old `randao.FallbackWindow`/old `FallbackSeedForEpoch(epoch)` — this is expected until Tasks 4–5 land. Confirm the *specific* failure is about the new symbols, not a leftover from a previous task.

- [ ] **Step 3: Implement**

In `jmdn/messaging/entropy_fallback_window.go`, add `"sort"` to imports, then replace the `FallbackFoldBufferB` doc comment's window description and add below it:

```go
// FallbackFoldMaxSlotOffset bounds how far past the cutoff K collection may
// run before giving up — the LIVENESS half of the design; FallbackFoldBufferB
// above is the SECURITY half (how many signers are required). The two are
// independent numbers on purpose: conflating them (treating B itself as a
// slot boundary) reintroduces exactly the halt bug this file exists to fix —
// a timeout inside a B-wide slot range would again leave the fold short.
//
// Derived from the same liveness rule that bounds B (T_vdf <= (n-k-maxOffset)/2
// * s_min) at the adopted N=50, K=3, s_min=60s, T_vdf=1200s:
//
//	maxOffset <= N - K - 2*T_vdf/s_min = 50 - 3 - 40 = 7
//
// Set to the ceiling itself: with FallbackFoldBufferB=5, this gives up to 2
// timed-out slots of tolerance while collecting the 5 required signers.
const FallbackFoldMaxSlotOffset = 7

var (
	// ErrFallbackNotYetReady means fewer than FallbackFoldBufferB signers have
	// been collected, but FallbackFoldMaxSlotOffset has not been reached
	// either. The caller must keep the epoch pending and retry on the next
	// committed block — see entropy_finalise.go's resolvePendingFallbacks.
	ErrFallbackNotYetReady = errors.New("messaging: fallback signer collection not yet complete")

	// ErrFallbackDeadlineExceeded means the collection deadline slot passed
	// with fewer than FallbackFoldBufferB signers collected. The epoch
	// produces no seed, permanently — there is no later point at which
	// retrying could still help, since retrying would mean using signers from
	// slots the liveness bound has already ruled unsafe to wait for.
	ErrFallbackDeadlineExceeded = errors.New("messaging: fallback collection deadline exceeded before enough signers were collected")
)
```

Replace `ValidateFallbackWindowParams`:

```go
func ValidateFallbackWindowParams() error {
	if _, _, err := randao.FallbackCollectionBounds(0, N, RevealCutoffK, FallbackFoldMaxSlotOffset); err != nil {
		return fmt.Errorf("messaging: N=%d/K=%d/MaxOffset=%d are not a usable fallback collection range: %w",
			N, RevealCutoffK, FallbackFoldMaxSlotOffset, err)
	}
	if FallbackFoldMaxSlotOffset < FallbackFoldBufferB {
		return fmt.Errorf("messaging: MaxOffset=%d is smaller than B=%d — even zero timeouts could never collect enough signers before the deadline",
			FallbackFoldMaxSlotOffset, FallbackFoldBufferB)
	}
	return nil
}
```

Replace `FallbackSeedForEpoch`:

```go
// FallbackSeedForEpoch attempts epoch's fallback seed at currentSlot.
//
// Three outcomes, and the caller (entropy_finalise.go) must handle all three
// distinctly:
//   - enough signers collected: returns the seed.
//   - fewer than FallbackFoldBufferB collected, FallbackFoldMaxSlotOffset not
//     yet reached: returns ErrFallbackNotYetReady. The epoch stays pending;
//     call again on the next committed block.
//   - fewer collected, deadline reached: returns ErrFallbackDeadlineExceeded.
//     The epoch produces no seed, and must not be retried again.
func FallbackSeedForEpoch(epoch, currentSlot uint64) (randao.Seed, error) {
	start, deadline, err := randao.FallbackCollectionBounds(epoch, N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if err != nil {
		return randao.Seed{}, err
	}

	defaultAggSigStore.mu.Lock()
	slots := make([]uint64, 0, len(defaultAggSigStore.sigs))
	for s := range defaultAggSigStore.sigs {
		if s >= start && s < deadline {
			slots = append(slots, s)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	collected := make([]randao.AggSig, 0, FallbackFoldBufferB)
	for _, s := range slots {
		if uint64(len(collected)) == FallbackFoldBufferB {
			break
		}
		collected = append(collected, randao.AggSig{Slot: s, Sig: defaultAggSigStore.sigs[s]})
	}
	defaultAggSigStore.mu.Unlock()

	if uint64(len(collected)) == FallbackFoldBufferB {
		return randao.FallbackFromCommittedSigners(BLS_Signer.DomainChainID(), epoch, start, deadline, FallbackFoldBufferB, collected)
	}
	if currentSlot < deadline {
		return randao.Seed{}, fmt.Errorf("%w: epoch %d has %d of %d signers, deadline slot %d (currentSlot=%d)",
			ErrFallbackNotYetReady, epoch, len(collected), FallbackFoldBufferB, deadline, currentSlot)
	}
	return randao.Seed{}, fmt.Errorf("%w: epoch %d reached slot %d (deadline %d) with only %d of %d signers",
		ErrFallbackDeadlineExceeded, epoch, currentSlot, deadline, len(collected), FallbackFoldBufferB)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd jmdn && go build ./messaging/... 2>&1 | head -30` — expect remaining errors only in `entropy_aggsig.go` (Task 4) and `entropy_finalise.go`/`entropy_finalise_test.go` (Task 5). If `entropy_fallback_window.go` or `entropy_fallback_window_test.go` themselves show an error, fix before proceeding — do not carry a broken Task 3 forward.

- [ ] **Step 5: Commit**

Do not commit yet — `jmdn/messaging` will not build as a whole package until Task 4 lands (Go requires the whole package to compile). Proceed directly to Task 4; commit at the end of Task 4 covers both.

---

## Task 4: `jmdn/messaging/entropy_aggsig.go` — migrate `CertificateForBlockAssembly`

**Files:**
- Modify: `jmdn/messaging/entropy_aggsig.go:127-148` (the `CertificateForBlockAssembly` function and its doc comment)

**Interfaces:**
- Consumes: `randao.FallbackCollectionBounds`, `FallbackFoldMaxSlotOffset` (Task 3).

- [ ] **Step 1: Update the function**

Replace lines 114–148 of `jmdn/messaging/entropy_aggsig.go`:

```go
// CertificateForBlockAssembly returns the certificate to attach to a block at
// `slot` with parent height `prevHeight`, or nil.
//
// Returns nil unless slot is inside the fallback collection deadline range —
// carrying it on every block would cost far more storage than only the range
// that might need it. The range is shifted by +1 slot to account for the
// one-block certificate lag: a block at slot S carries the certificate for
// its PARENT, so to cover collection slots [K, K+MaxOffset) the certificates
// ride on the blocks that follow them.
//
// Note this range can be wider than FallbackFoldBufferB slots — collection
// stops as soon as B signers are found, wherever in the range that happens,
// so a block anywhere before the deadline might turn out to be one of the
// B signers actually used. A timed-out round simply leaves its slot
// uncovered, which is expected and does not block collection.
func CertificateForBlockAssembly(slot, prevHeight uint64) []config.CertSigner {
	if !AggCertEnabled {
		return nil
	}
	epoch := EpochForSlot(slot)
	start, deadline, err := randao.FallbackCollectionBounds(epoch, N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if err != nil {
		return nil
	}
	if slot < start+1 || slot >= deadline+1 {
		return nil // this block's parent is not in the collection range
	}

	certForNextBlockMu.Lock()
	defer certForNextBlockMu.Unlock()
	if certForNextHeight != prevHeight || len(certForNextBlock) == 0 {
		return nil // we do not hold the certificate for this block's parent
	}
	out := make([]config.CertSigner, len(certForNextBlock))
	copy(out, certForNextBlock)
	return out
}
```

- [ ] **Step 2: Build the package**

Run: `cd jmdn && go build ./messaging/... 2>&1 | head -30`
Expected: errors remain only in `entropy_finalise.go`/`entropy_finalise_test.go` (Task 5's scope — old `finaliseEpoch`/`resolveFallbackSeed` still call the old `FallbackSeedForEpoch(epoch)` one-argument form).

- [ ] **Step 3: Run the existing `entropy_aggsig_test.go` suite**

Run: `cd jmdn && go test ./messaging/ -run TestPrevSlot -v` (this exercises the P+1 fix from earlier in the session, unrelated to this change, and must still pass — it is the regression guard for a different bug and this task must not touch it).
Expected: PASS.

- [ ] **Step 4: Commit**

Do not commit yet — hold until Task 5 makes the whole `messaging` package build again.

---

## Task 5: `jmdn/messaging/entropy_finalise.go` — the two-phase split

**Files:**
- Modify: `jmdn/messaging/entropy_finalise.go`
- Modify: `jmdn/messaging/entropy_finalise_test.go`

**Interfaces:**
- Consumes: `FallbackSeedForEpoch(epoch, currentSlot uint64)` (Task 3), `ErrFallbackNotYetReady`, `ErrFallbackDeadlineExceeded` (Task 3), existing `entropyAccumulatorFor`, `cutoffSlotFor`, `epochsWithClosedRevealWindow`, `notifyEpochFinalised`, `pruneAggSigsBelow`, `pruneRevealsBelow` (all unchanged).
- Produces: `decideEpoch(epoch uint64, block *config.ZKBlock)`, `resolvePendingFallbacks(block *config.ZKBlock)` — replace `finaliseEpoch` and `resolveFallbackSeed`, which are deleted.

- [ ] **Step 1: Write the failing tests**

Replace the `resolveFallbackSeed`-based tests in `entropy_finalise_test.go` (`TestResolveFallbackSeed_*`) with tests against the new pending-state lifecycle:

```go
func resetFinaliseTracking(t *testing.T) {
	t.Helper()
	finaliseTrackMu.Lock()
	savedDecided, savedHave, savedPending := lastDecidedEpoch, haveDecidedAny, pendingFallback
	lastDecidedEpoch, haveDecidedAny = 0, false
	pendingFallback = make(map[uint64]struct{})
	finaliseTrackMu.Unlock()
	t.Cleanup(func() {
		finaliseTrackMu.Lock()
		lastDecidedEpoch, haveDecidedAny, pendingFallback = savedDecided, savedHave, savedPending
		finaliseTrackMu.Unlock()
	})
}

// A fallback outcome at the cutoff must NOT try to resolve a seed
// immediately — that is the exact bug (finalisation firing when the
// collection range holds nothing) this plan exists to fix. It must instead
// become pending.
func TestDecideEpoch_FallbackOutcomeBecomesPendingNotImmediatelyResolved(t *testing.T) {
	resetFinaliseTracking(t)
	resetEntropyAccumulatorStore(t)
	wireEligibilityWithPeers(t, []string{"peer-a", "peer-b"}) // 2 expected, none revealed -> fallback
	withBeaconEntropy(t, map[uint64][]byte{20: fakeEntropy(0x11, 32)})

	var notified bool
	SetEpochFinalisedHook(func(uint64, randao.Seed) { notified = true })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	decideEpoch(20, &config.ZKBlock{Slot: cutoffSlotFor(20), BlockNumber: 1})

	finaliseTrackMu.Lock()
	_, isPending := pendingFallback[20]
	finaliseTrackMu.Unlock()

	if !isPending {
		t.Fatal("epoch 20 had a fallback outcome but was not marked pending")
	}
	if notified {
		t.Fatal("epoch 20 must not finalise yet — no signers exist at the cutoff slot")
	}
}

// Once B signers have been collected, resolvePendingFallbacks must seal the
// epoch on a LATER block — this is the scenario that was previously
// impossible under any parameterisation.
func TestResolvePendingFallbacks_SealsOncEnoughSignersArrive(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)

	finaliseTrackMu.Lock()
	pendingFallback[7] = struct{}{}
	finaliseTrackMu.Unlock()

	start, _, err := randao.FallbackCollectionBounds(7, N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if err != nil {
		t.Fatalf("bounds: %v", err)
	}
	for i := uint64(0); i < FallbackFoldBufferB; i++ {
		mustRecordAggSig(t, start+i, fixedSig(byte(i+1)))
	}

	var gotEpoch uint64
	var gotSeed randao.Seed
	SetEpochFinalisedHook(func(e uint64, s randao.Seed) { gotEpoch, gotSeed = e, s })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	resolvePendingFallbacks(&config.ZKBlock{Slot: start + FallbackFoldBufferB - 1, BlockNumber: 2})

	if gotEpoch != 7 {
		t.Fatalf("hook fired for epoch %d, want 7", gotEpoch)
	}
	if gotSeed == (randao.Seed{}) {
		t.Fatal("hook fired with the zero seed")
	}
	finaliseTrackMu.Lock()
	_, stillPending := pendingFallback[7]
	finaliseTrackMu.Unlock()
	if stillPending {
		t.Fatal("epoch 7 sealed but was left in the pending set")
	}
}

// Past the deadline with too few signers: the epoch must be dropped from
// pending and never retried, with no seed produced.
func TestResolvePendingFallbacks_DropsEpochAtDeadlineWithoutASeed(t *testing.T) {
	resetFinaliseTracking(t)
	resetAggSigStore(t)

	finaliseTrackMu.Lock()
	pendingFallback[3] = struct{}{}
	finaliseTrackMu.Unlock()

	_, deadline, err := randao.FallbackCollectionBounds(3, N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if err != nil {
		t.Fatalf("bounds: %v", err)
	}

	var notified bool
	SetEpochFinalisedHook(func(uint64, randao.Seed) { notified = true })
	t.Cleanup(func() { SetEpochFinalisedHook(nil) })

	resolvePendingFallbacks(&config.ZKBlock{Slot: deadline, BlockNumber: 5})

	if notified {
		t.Fatal("epoch 3 must not finalise — it never collected enough signers")
	}
	finaliseTrackMu.Lock()
	_, stillPending := pendingFallback[3]
	finaliseTrackMu.Unlock()
	if stillPending {
		t.Fatal("epoch 3 exceeded its deadline but was left in the pending set — it would be retried forever")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd jmdn && go test ./messaging/ -run 'TestDecideEpoch|TestResolvePendingFallbacks' -v`
Expected: FAIL — build error (`decideEpoch`, `resolvePendingFallbacks`, `pendingFallback`, `lastDecidedEpoch` undefined).

- [ ] **Step 3: Implement**

In `jmdn/messaging/entropy_finalise.go`, replace the `resolveFallbackSeed` function, the `fallbackSeedSource`/`fallbackSourceAggSig` block, `finaliseEpoch`, the `finaliseTrackMu`/`lastFinalisedEpoch`/`haveFinalisedAny` var block, and `maybeFinaliseCompletedEpochs` with:

```go
// ---------------------------------------------------------------------------
// Two-phase finalisation
// ---------------------------------------------------------------------------
//
// Split 2026-08-24 to fix a bug found while reviewing the count-based
// collection design: the single-shot version of this file called
// FallbackSeedForEpoch AT the cutoff slot — the exact instant the collection
// range opens and holds zero signers. Every fallback epoch failed closed
// unconditionally; the fallback path could never succeed, regardless of B or
// how the window was shaped.
//
// decideEpoch now makes ONLY the mixed-vs-fallback call at the cutoff. A
// fallback outcome enters pendingFallback instead of being resolved
// immediately. resolvePendingFallbacks re-attempts every pending epoch on
// every subsequently committed block, because the signers a pending epoch
// needs arrive on those later blocks, not before.
var (
	finaliseTrackMu   sync.Mutex
	lastDecidedEpoch  uint64
	haveDecidedAny    bool
	pendingFallback   = make(map[uint64]struct{})
)

// decideEpoch makes the one decision Architecture §7.2 ties to the cutoff
// slot: mixed, or fallback. A mixed outcome finalises immediately, exactly as
// before. A fallback outcome is marked pending — see this section's header
// for why it must not try to resolve a seed here.
func decideEpoch(epoch uint64, block *config.ZKBlock) {
	acc, err := entropyAccumulatorFor(epoch)
	if err != nil {
		log.Error().Err(err).Uint64("epoch", epoch).Uint64("height", block.BlockNumber).
			Msg("entropy: cannot decide epoch outcome — no accumulator")
		return
	}

	res := acc.Finalise()
	if res.Outcome != randao.OutcomeFallback {
		notifyEpochFinalised(epoch, res.Seed)
		pruneAggSigsBelow(cutoffSlotFor(epoch))
		pruneRevealsBelow(epoch + 1)
		return
	}

	finaliseTrackMu.Lock()
	pendingFallback[epoch] = struct{}{}
	finaliseTrackMu.Unlock()
	log.Info().Uint64("epoch", epoch).Strs("withheld", res.Withheld).Uint64("height", block.BlockNumber).
		Msg("entropy: epoch entered fallback at the reveal cutoff — collecting aggregate-signature signers before it can finalise")
}

// resolvePendingFallbacks re-attempts every epoch still waiting on the
// aggregate-signature fold, at this block's slot. Called on every committed
// block (not just at the cutoff) via maybeFinaliseCompletedEpochs, because a
// pending epoch's signers are exactly the blocks committed after its cutoff.
func resolvePendingFallbacks(block *config.ZKBlock) {
	finaliseTrackMu.Lock()
	pending := make([]uint64, 0, len(pendingFallback))
	for e := range pendingFallback {
		pending = append(pending, e)
	}
	finaliseTrackMu.Unlock()
	sort.Slice(pending, func(i, j int) bool { return pending[i] < pending[j] })

	for _, e := range pending {
		seed, err := FallbackSeedForEpoch(e, block.Slot)
		switch {
		case err == nil:
			finaliseTrackMu.Lock()
			delete(pendingFallback, e)
			finaliseTrackMu.Unlock()
			log.Info().Uint64("epoch", e).Uint64("height", block.BlockNumber).
				Msg("entropy: epoch finalised via the §4.2a aggregate-signature fallback")
			notifyEpochFinalised(e, seed)
			pruneAggSigsBelow(cutoffSlotFor(e))
			pruneRevealsBelow(e + 1)
		case errors.Is(err, ErrFallbackNotYetReady):
			// Still collecting; try again on the next block.
		case errors.Is(err, ErrFallbackDeadlineExceeded):
			finaliseTrackMu.Lock()
			delete(pendingFallback, e)
			finaliseTrackMu.Unlock()
			log.Error().Err(err).Uint64("epoch", e).Uint64("height", block.BlockNumber).
				Msg("entropy: fallback deadline exceeded — no seed produced for this epoch (fail closed by design; not retried again)")
			pruneAggSigsBelow(cutoffSlotFor(e))
			pruneRevealsBelow(e + 1)
		default:
			log.Error().Err(err).Uint64("epoch", e).Uint64("height", block.BlockNumber).
				Msg("entropy: unexpected error resolving a pending fallback epoch")
		}
	}
}

// maybeFinaliseCompletedEpochs runs both phases for a newly committed block:
// decide any epoch whose reveal cutoff this block's slot has just reached,
// then retry every still-pending fallback epoch against this block.
//
// Call once per committed block, from the same two hooks
// foldBlockDeclaredReveals uses (broadcast.go's ProcessBlockLocally,
// blockPropagation.go's receive path) — and AFTER foldBlockDeclaredReveals and
// VerifyAndRecordPrevCert, so this block's own reveal and its parent's
// certificate are in before either phase runs.
func maybeFinaliseCompletedEpochs(block *config.ZKBlock) {
	finaliseTrackMu.Lock()
	toDecide := epochsWithClosedRevealWindow(block.Slot, lastDecidedEpoch, haveDecidedAny)
	finaliseTrackMu.Unlock()

	for _, e := range toDecide {
		decideEpoch(e, block)
		finaliseTrackMu.Lock()
		lastDecidedEpoch = e
		haveDecidedAny = true
		finaliseTrackMu.Unlock()
	}

	resolvePendingFallbacks(block)
}
```

Add `"errors"` and `"sort"` to the file's imports (both are new to this file — `errors` was previously only needed by the deleted `resolveFallbackSeed`'s caller-side wrapping, `sort` is new for the pending-epoch ordering).

Delete: the old `fallbackSeedSource` type, `fallbackSourceAggSig` const, `resolveFallbackSeed` function, `finaliseEpoch` function, and the old `finaliseTrackMu`/`lastFinalisedEpoch`/`haveFinalisedAny` var block (all superseded by the code above).

- [ ] **Step 4: Delete/rewrite the tests that targeted removed functions**

In `entropy_finalise_test.go`: delete `TestResolveFallbackSeed_UsesAggSigWindow`, `TestResolveFallbackSeed_FailsClosedWithNoAggSigWindow`, `TestResolveFallbackSeed_NeverReturnsTheBrokenFormula`, `seedTag`/`errNoAggSig`/`aggSigOK`/`aggSigFails` if nothing else in the package still uses them (check with `grep -rn 'aggSigOK\|aggSigFails\|seedTag' jmdn/messaging/*.go` — `seedTag` is likely still useful as a fixture and can stay). Add the three tests from Step 1 above. Leave `TestEpochsWithClosedRevealWindow_*` and the `TestNotifyEpochFinalised_*`/`TestSetEpochFinalisedHook_*` tests untouched — none of them exercise the removed functions.

- [ ] **Step 5: Run the full `messaging` package test suite**

Run: `cd jmdn && go build ./... && go test ./... -v 2>&1 | tail -80`
Expected: package builds; every test passes, including the pre-existing `TestPrevSlotAccountsForPeriod`, `TestEndToEnd_ProduceToBlockToFold`, and everything in `entropy_reveal_inbox_test.go` untouched by this plan.

- [ ] **Step 6: Commit**

```bash
cd jmdn && git add messaging/entropy_fallback_window.go messaging/entropy_fallback_window_test.go \
  messaging/entropy_aggsig.go messaging/entropy_finalise.go messaging/entropy_finalise_test.go
git commit -m "messaging: two-phase fallback finalisation — count-based collection with a separate slot deadline"
```

- [ ] **Step 7: Sync to device**

Stage all five files above via `device_stage_files`, verify with `device_list_dir`/a diff read, then `device_commit_files` into the matching `~/Block/jmdn/messaging/` paths.

---

## Task 6: Full cross-repo verification + spec + `verify-m4.sh`

**Files:**
- Modify: `verify-m4.sh` (add one check)
- Modify: `~/Block/JMDT-Knowledgebase/AVC-Architecture-End-to-End.md` (record the amendment to decision 11)

**Interfaces:** none new — this task only verifies and documents what Tasks 1–5 built.

- [ ] **Step 1: Full build/test on both repos**

Run: `cd avc && go build ./... && go test ./... -v 2>&1 | tail -40`
Run: `cd jmdn && go build ./... && go test ./... -v 2>&1 | tail -80`
Expected: both clean. If `avc` is clean but `jmdn` fails on an import of a deleted `randao` symbol, grep for it: `grep -rn 'FallbackWindow\|FallbackFromAggSigs' jmdn/` — every remaining hit is a caller Task 4/5 missed and must be fixed before continuing.

- [ ] **Step 2: Add a `verify-m4.sh` regression check for the fixed bug**

Add to `verify-m4.sh`, in section 3 ("WHAT HAPPENS TODAY IF AN EPOCH FINALISES?"):

```bash
n=$(grep -rn 'func resolvePendingFallbacks' --include=\*.go jmdn/messaging 2>/dev/null | wc -l | tr -d ' ')
if [ "$n" -gt 0 ]; then ok "two-phase fallback finalisation present (decideEpoch/resolvePendingFallbacks)"
else bad "still single-shot finalisation -- the 'fallback can never succeed' bug is back"; fi
if grep -q 'FallbackFoldMaxSlotOffset' jmdn/messaging/entropy_fallback_window.go 2>/dev/null; then
  ok "fallback collection has a separate slot deadline, distinct from B"
else bad "no slot deadline constant found -- B may have been collapsed back into a slot boundary"; fi
if grep -q 'func FallbackCollectionBounds' avc/randao/fallback_aggsig.go 2>/dev/null; then
  ok "avc/randao exposes count-tolerant collection bounds"
else bad "avc/randao still has only the old fixed-width FallbackWindow"; fi
```

- [ ] **Step 3: Run the updated script against the device tree**

Stage the updated `verify-m4.sh` to the device (or run directly against `/root/work` first with `bash verify-m4.sh /root/work`), then run `bash verify-m4.sh ~/Block` via `device_bash` against the real repos once Tasks 1–5 are pushed.
Expected: the three new checks pass; no previously-passing check regresses. Read the full pass/fail count, not just the new lines.

- [ ] **Step 4: Update the architecture doc**

In `~/Block/JMDT-Knowledgebase/AVC-Architecture-End-to-End.md`, under §10 decision 11, add a dated amendment (do not rewrite the original decision — append, so the history of what changed and why stays visible):

```markdown
**Amendment, 2026-08-24:** The fold window [K, K+B) originally required a
committed, certified block at every slot in the range — a single timed-out
round made the epoch's fallback permanently uncomputable (a halt vector).
Amended to a count-based collection: B stays the number of required signers;
a new, separate parameter FallbackFoldMaxSlotOffset (=7, at the same liveness
ceiling B was already bounded by) is the deadline past which collection gives
up. Finalisation was also split into two phases (decide mixed-vs-fallback at
the cutoff; resolve the fallback seed later, once collection completes or
times out) — the single-shot version called the fold at the cutoff instant,
when it always held zero signers, so the fallback path could not succeed
under any prior parameterisation.
```

- [ ] **Step 5: Commit and push**

```bash
cd jmdn && git add verify-m4.sh 2>/dev/null; git status
```

(Note: `verify-m4.sh` lives at the repo root one level above both `jmdn` and `avc` per its own `cd "$(dirname "$0")"` / `ROOT="${1:-$HOME/Block}"` logic — confirm its actual tracked location with `git -C ~/Block status verify-m4.sh` before committing, since it is not nested inside either individual repo.)

Stage and commit the architecture doc change separately if `JMDT-Knowledgebase` is its own git repo; otherwise sync it via `device_stage_files`/`device_commit_files` like the other non-code files this session has produced.

---

## Self-Review

**Spec coverage:** every element of the design discussed this session is covered — B stays a count (Tasks 1–3), a separate slot deadline exists (Task 3), the two-phase split fixes the "fallback can never succeed" bug (Task 5), the cert-carrying window is migrated so it doesn't silently keep using the deleted fixed-width function (Task 4), and both the automated regression guard and the architecture doc are updated (Task 6). Nothing from the discussion was left uncovered.

**Placeholder scan:** every step above has literal Go code, not a description of code. No "TODO"/"similar to Task N"/"add appropriate error handling" appears anywhere in the task bodies.

**Type consistency:** `FallbackSeedForEpoch(epoch, currentSlot uint64)` (Task 3) is the signature used consistently in Task 5's `resolvePendingFallbacks` and its tests. `FallbackCollectionBounds(epoch, n, k, maxOffset uint64) (start, deadline uint64, err error)` (Task 1) is used identically in Tasks 3, 4, and the Task 5 tests. `FallbackFromCommittedSigners(chainID, epoch, start, deadline, b uint64, sigs []AggSig) (Seed, error)` (Task 2) is used identically in Task 3. No name drifted between where it was defined and where it was consumed.

**One known gap this plan does not close:** the proposer-rotation lower bound on B (task #8 in the wider backlog) is still unscoped, so even after this plan lands, B=5 (or any B) cannot yet be proven to span more than one proposer's turn. That is out of scope here and was already flagged as a separate open item before this plan was written.
