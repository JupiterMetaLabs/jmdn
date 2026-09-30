package messaging

// Restart-recovery tests for the entropy pipeline (entropy_recovery.go).
//
// The harness drives the REAL pipeline: real ed25519 identities and reveals,
// real randao Accumulators, the real decide/fallback code and the real mix
// store. Only storage (the mix KV) and the chain (a height->block map) are
// in-memory. A "restart" wipes every piece of process memory the pipeline
// keeps, exactly what a process restart does.
//
// Chain layout (N=50, K=3, height == slot, period 0):
//   epoch 7: reveal window slots 350..352, cutoff 353
//   epoch 8: reveal window slots 400..402, cutoff 403

import (
	"errors"
	"sync"
	"testing"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"

	"github.com/JupiterMetaLabs/avc/randao"

	"gossipnode/config"
)

type hookCall struct {
	epoch uint64
	seed  randao.Seed
}

type entropyWorld struct {
	t       *testing.T
	privs   []ic.PrivKey
	ids     []string
	blocks  map[uint64]*config.ZKBlock
	stored  uint64 // highest height in the "database"
	mu      sync.Mutex
	kv      map[uint64]randao.Seed
	persist []uint64 // order of persist calls
	hooks   []hookCall
	events  []string // "persist:E" / "hook:E" in call order
}

const (
	worldFirstHeight = 300
	worldMembers     = 5
)

func newEntropyWorld(t *testing.T) *entropyWorld {
	t.Helper()
	w := &entropyWorld{t: t, blocks: map[uint64]*config.ZKBlock{}, kv: map[uint64]randao.Seed{}, stored: 420}
	for i := 0; i < worldMembers; i++ {
		p, id := newTestIdentity(t)
		w.privs = append(w.privs, p)
		w.ids = append(w.ids, id)
	}

	// Global state isolation.
	resetEntropyAccumulatorStore(t)
	resetFinaliseTracking(t)
	ResetMixStoreForTest()
	t.Cleanup(ResetMixStoreForTest)
	ResetAggSigStoreForTest()
	t.Cleanup(ResetAggSigStoreForTest)
	savedSlot, savedPeriod := DefaultSlotStore, DefaultPeriodStore
	DefaultSlotStore, DefaultPeriodStore = NewSlotStore(), NewPeriodStore()
	savedHW := committedSlotHW.Load()
	committedSlotHW.Store(0)
	t.Cleanup(func() {
		DefaultSlotStore, DefaultPeriodStore = savedSlot, savedPeriod
		committedSlotHW.Store(savedHW)
	})
	disarmEntropyRecovery()
	t.Cleanup(disarmEntropyRecovery)

	withBeaconEntropy(t, map[uint64][]byte{7: fakeEntropy(0x77, 32), 8: fakeEntropy(0x88, 32)})
	wireEligibilityWithPeers(t, w.ids)

	prevPersist, prevLoad := entropyMixPersist, entropyMixLoad
	entropyMixPersist = func(e uint64, s randao.Seed, _ string) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.persist = append(w.persist, e)
		w.events = append(w.events, "persist")
		if prev, ok := w.kv[e]; ok {
			if prev != s {
				return errors.New("DIFFERENT value")
			}
			return nil
		}
		w.kv[e] = s
		return nil
	}
	entropyMixLoad = func(e uint64) (randao.Seed, bool, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		s, ok := w.kv[e]
		return s, ok, nil
	}
	SetEpochFinalisedHook(func(e uint64, s randao.Seed) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.hooks = append(w.hooks, hookCall{e, s})
		w.events = append(w.events, "hook")
	})
	t.Cleanup(func() {
		entropyMixPersist, entropyMixLoad = prevPersist, prevLoad
		SetEpochFinalisedHook(nil)
	})

	// Blocks 300..420, reveals in both windows (every member reveals).
	reveals := map[uint64][]int{
		350: {0, 1}, 351: {2, 3}, 352: {4},
		400: {0, 1, 2}, 401: {3, 4},
	}
	for h := uint64(worldFirstHeight); h <= 420; h++ {
		b := &config.ZKBlock{BlockNumber: h, Slot: h}
		for _, m := range reveals[h] {
			b.RandaoReveals = append(b.RandaoReveals, config.Reveal{
				ProposerID: w.ids[m], Secret: w.revealFor(m, EpochForSlot(h)),
			})
		}
		w.blocks[h] = b
	}
	return w
}

// revealFor produces member m's reveal through the production path.
func (w *entropyWorld) revealFor(m int, epoch uint64) []byte {
	w.t.Helper()
	nodeIdentityMu.Lock()
	sp, si := nodeIdentityPriv, nodeIdentityPeer
	nodeIdentityMu.Unlock()
	defer func() {
		nodeIdentityMu.Lock()
		nodeIdentityPriv, nodeIdentityPeer = sp, si
		nodeIdentityMu.Unlock()
	}()
	if err := SetNodeIdentity(w.privs[m], w.ids[m]); err != nil {
		w.t.Fatal(err)
	}
	sig, err := ProduceRevealForEpoch(epoch)
	if err != nil {
		w.t.Fatalf("ProduceRevealForEpoch(%d) for member %d: %v", epoch, m, err)
	}
	return sig
}

func (w *entropyWorld) getBlock(h uint64) (*config.ZKBlock, error) {
	b, ok := w.blocks[h]
	if !ok || h > w.stored {
		return nil, errors.New("not stored")
	}
	return b, nil
}

// longRunning puts the watermark where a node that has been up for a while
// has it, so epochs 0..6 (no entropy in this harness) are not re-decided.
func longRunning() {
	finaliseTrackMu.Lock()
	lastDecidedEpoch, haveDecidedAny = 6, true
	finaliseTrackMu.Unlock()
}

func (w *entropyWorld) applyLive(from, to uint64) {
	for h := from; h <= to; h++ {
		if h > w.stored {
			w.stored = h // stored before its effects are applied, as on a node
		}
		ApplyBlockEntropyEffects(w.blocks[h])
	}
}

// restart wipes all process memory the entropy pipeline holds. The KV (w.kv)
// and the chain survive, as on a real node.
func (w *entropyWorld) restart(tip uint64) {
	defaultEntropyAccumulatorStore.mu.Lock()
	defaultEntropyAccumulatorStore.accs = map[uint64]*randao.Accumulator{}
	defaultEntropyAccumulatorStore.mu.Unlock()
	finaliseTrackMu.Lock()
	lastDecidedEpoch, haveDecidedAny = 0, false
	pendingFallback = map[uint64]struct{}{}
	finaliseTrackMu.Unlock()
	ResetMixStoreForTest()
	ResetAggSigStoreForTest()
	committedSlotHW.Store(0)
	DefaultSlotStore = NewSlotStore()
	DefaultSlotStore.SeedFromCommittedTip(tip, tip) // RecoverSlotStoreAtStartup
	w.stored = tip
	w.mu.Lock()
	w.hooks, w.events, w.persist = nil, nil, nil
	w.mu.Unlock()
}

func (w *entropyWorld) hookCalls() []hookCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]hookCall(nil), w.hooks...)
}

func mustMix(t *testing.T, e uint64) randao.Seed {
	t.Helper()
	s, ok := FinalisedMixFor(e)
	if !ok {
		t.Fatalf("no mix for epoch %d", e)
	}
	return s
}

// baseline runs an uninterrupted node over the whole chain.
func (w *entropyWorld) baseline() (m7, m8 randao.Seed) {
	w.t.Helper()
	longRunning()
	w.applyLive(340, 420)
	m7, m8 = mustMix(w.t, 7), mustMix(w.t, 8)
	if m7 == m8 {
		w.t.Fatal("harness: two epochs produced the same mix")
	}
	return m7, m8
}

// ---------------------------------------------------------------------------

// The divergence this change exists to fix: a restart INSIDE epoch 8's reveal
// window. Without replay, the reveals committed before the restart are lost
// and the epoch does not finalise to the fleet's mix. With replay it does.
func TestEntropyRestartInsideRevealWindow_ReplayRecoversTheFleetMix(t *testing.T) {
	w := newEntropyWorld(t)
	_, want8 := w.baseline()
	want7 := mustMix(t, 7)

	// --- old behaviour: restart at 400, no replay, keep going live.
	w.restart(400)
	w.applyLive(401, 420)
	if got, ok := FinalisedMixFor(8); ok && got == want8 {
		t.Fatal("harness is not sensitive: the no-replay path reproduced the fleet mix, so this test proves nothing")
	}

	// --- new behaviour: restart at 400, replay, keep going live.
	w.kv = map[uint64]randao.Seed{} // no durable record either: pure chain replay
	w.restart(400)
	ArmEntropyRecovery(time.Minute)
	rep, err := RunEntropyStartupRecovery(true, 400, true, w.getBlock)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if rep.FromEpoch != 7 || rep.TipEpoch != 8 || rep.FromHeight != 350 || rep.Blocks != 51 {
		t.Fatalf("replay range: %+v", rep)
	}
	if got := mustMix(t, 7); got != want7 {
		t.Fatalf("epoch 7 mix after replay differs from the uninterrupted node")
	}
	w.applyLive(401, 420)
	if got := mustMix(t, 8); got != want8 {
		t.Fatalf("epoch 8 mix after a mid-window restart differs from the uninterrupted node")
	}
	// Sealing: resumed once for epoch 7 after the replay, then epoch 8 live.
	calls := w.hookCalls()
	if len(calls) != 2 || calls[0].epoch != 7 || calls[0].seed != want7 || calls[1].epoch != 8 || calls[1].seed != want8 {
		t.Fatalf("hook calls after restart: %+v", calls)
	}
}

// Restart after the cutoff: replay alone (no durable record) re-derives exactly
// the uninterrupted node's mixes, and a second replay gives the same answer.
func TestEntropyReplayIsDeterministicAndEqualsLive(t *testing.T) {
	w := newEntropyWorld(t)
	want7, want8 := w.baseline()

	for round := 0; round < 2; round++ {
		if round == 0 {
			w.kv = map[uint64]randao.Seed{}
		}
		w.restart(410)
		rep, err := RunEntropyStartupRecovery(true, 410, true, w.getBlock)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if mustMix(t, 7) != want7 || mustMix(t, 8) != want8 {
			t.Fatalf("round %d: replayed mixes differ from live", round)
		}
		if round == 1 && rep.MixesRestored != 2 {
			t.Fatalf("round 1 should restore the 2 mixes round 0 persisted, got %d", rep.MixesRestored)
		}
		calls := w.hookCalls()
		if len(calls) != 1 || calls[0].epoch != 8 || calls[0].seed != want8 || !rep.Resumed || rep.ResumedEpoch != 8 {
			t.Fatalf("round %d: sealing must resume exactly once, for epoch 8; calls=%+v rep=%+v", round, calls, rep)
		}
	}
}

// A durable record that disagrees with the replay keeps the retained value
// (notifyEpochFinalised's existing rule) and seals from it.
func TestEntropyRecoveryPersistedMixWinsOverReplayConflict(t *testing.T) {
	w := newEntropyWorld(t)
	w.baseline()
	stored := seedTag(0xEE)
	w.kv[8] = stored

	w.restart(410)
	if _, err := RunEntropyStartupRecovery(true, 410, true, w.getBlock); err != nil {
		t.Fatal(err)
	}
	if got := mustMix(t, 8); got != stored {
		t.Fatal("the retained (persisted) mix must win over a conflicting replay")
	}
	calls := w.hookCalls()
	if len(calls) != 1 || calls[0].seed != stored {
		t.Fatalf("sealing must use the retained mix; calls=%+v", calls)
	}
}

// Epoch decisions are paused between arming and the replay; folding is not.
func TestEntropyStartupGatePausesDecisionsUntilReplay(t *testing.T) {
	w := newEntropyWorld(t)
	_, want8 := w.baseline()

	w.kv = map[uint64]randao.Seed{}
	w.restart(403)
	ArmEntropyRecovery(time.Minute)
	w.applyLive(404, 404) // arrives before the replay
	if _, ok := FinalisedMixFor(8); ok {
		t.Fatal("an epoch was decided while the startup gate was armed")
	}
	finaliseTrackMu.Lock()
	have := haveDecidedAny
	finaliseTrackMu.Unlock()
	if have {
		t.Fatal("the watermark advanced while the startup gate was armed")
	}

	if _, err := RunEntropyStartupRecovery(true, 404, true, w.getBlock); err != nil {
		t.Fatal(err)
	}
	if entropyRecoveryArmed.Load() {
		t.Fatal("recovery must disarm the gate")
	}
	if got := mustMix(t, 8); got != want8 {
		t.Fatal("epoch 8 after gated start differs from the uninterrupted node")
	}
}

func TestEntropyStartupGateAlwaysDisarms(t *testing.T) {
	newEntropyWorld(t)

	ArmEntropyRecovery(time.Minute)
	if _, err := RunEntropyStartupRecovery(false, 0, false, nil); err != nil {
		t.Fatal(err)
	}
	if entropyRecoveryArmed.Load() {
		t.Fatal("Stage 1 (no beacon) must still disarm")
	}

	ArmEntropyRecovery(time.Minute)
	if _, err := RunEntropyStartupRecovery(true, 500, true, func(uint64) (*config.ZKBlock, error) {
		return nil, errors.New("db down")
	}); err == nil {
		t.Fatal("an unreadable tip must be reported")
	}
	if entropyRecoveryArmed.Load() {
		t.Fatal("a failed recovery must still disarm")
	}

	ArmEntropyRecovery(20 * time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for entropyRecoveryArmed.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if entropyRecoveryArmed.Load() {
		t.Fatal("the arm timer must disarm when recovery never runs")
	}
}

// The sync path now decides epochs (so a synced node holds each mix) but never
// fires the sealing hook.
func TestSyncedBlocksDecideQuietly(t *testing.T) {
	w := newEntropyWorld(t)
	want7, want8 := w.baseline()

	w.restart(339)
	longRunning()
	for h := uint64(340); h <= 420; h++ {
		RecordSyncedBlockEntropy(w.blocks[h])
	}
	if mustMix(t, 7) != want7 || mustMix(t, 8) != want8 {
		t.Fatal("synced node derived different mixes from the live node")
	}
	if calls := w.hookCalls(); len(calls) != 0 {
		t.Fatalf("the sync path must not start sealing; hook calls=%+v", calls)
	}
}

// The durable mix is written BEFORE the sealing hook runs, so a crash during a
// seal still leaves the mix on disk.
func TestMixPersistedBeforeSealingHook(t *testing.T) {
	w := newEntropyWorld(t)
	longRunning()
	w.applyLive(340, 353) // epoch 7 decides at 353
	w.mu.Lock()
	ev := append([]string(nil), w.events...)
	w.mu.Unlock()
	if len(ev) != 2 || ev[0] != "persist" || ev[1] != "hook" {
		t.Fatalf("want [persist hook], got %v", ev)
	}
}

func TestEntropyReplayScanFailsClosed(t *testing.T) {
	w := newEntropyWorld(t)

	delete(w.blocks, 360)
	if _, err := ReplayEntropyFromChain(410, w.getBlock); err == nil {
		t.Fatal("a missing committed block inside the replay range must fail the replay")
	}
	w.blocks[360] = &config.ZKBlock{BlockNumber: 360, Slot: 361} // not below its successor's slot
	if _, err := ReplayEntropyFromChain(410, w.getBlock); err == nil {
		t.Fatal("non-increasing slots must fail the replay")
	}
	if entropyReplayActive.Load() || entropyQuietFinalise.Load() {
		t.Fatal("replay flags must be cleared after a failed replay")
	}
}

// A block delivered after node.NewNode() but before the gate is armed (e.g.
// before the beacon is installed) can still advance the watermark past the
// current epoch with nothing decided. The replay must reset it, or epoch 8
// would never be decided on this node.
func TestEntropyReplayResetsAStaleWatermark(t *testing.T) {
	w := newEntropyWorld(t)
	_, want8 := w.baseline()

	w.kv = map[uint64]randao.Seed{}
	w.restart(410)
	finaliseTrackMu.Lock()
	lastDecidedEpoch, haveDecidedAny = 8, true // claimed, never decided
	finaliseTrackMu.Unlock()

	if _, err := RunEntropyStartupRecovery(true, 410, true, w.getBlock); err != nil {
		t.Fatal(err)
	}
	if got, ok := FinalisedMixFor(8); !ok || got != want8 {
		t.Fatal("replay did not re-decide epoch 8 behind a stale watermark")
	}
}

// main() reads the tip before the replay takes its lock. A block stored in
// between is folded under the gate; the replay must include it (forward walk),
// or its reveals are lost when the accumulator is rebuilt.
func TestEntropyReplayIncludesBlocksStoredAfterTheTipWasRead(t *testing.T) {
	w := newEntropyWorld(t)
	_, want8 := w.baseline()

	w.kv = map[uint64]randao.Seed{}
	w.restart(400)
	ArmEntropyRecovery(time.Minute)
	w.applyLive(401, 401) // arrives before the replay: folded, not decided

	rep, err := RunEntropyStartupRecovery(true, 400, true, w.getBlock) // stale tip
	if err != nil {
		t.Fatal(err)
	}
	if rep.TipHeight != 401 {
		t.Fatalf("replay must extend to the stored tip 401, got %d", rep.TipHeight)
	}
	w.applyLive(402, 420)
	if got := mustMix(t, 8); got != want8 {
		t.Fatal("reveals from a block stored after the tip was read were lost")
	}
}
