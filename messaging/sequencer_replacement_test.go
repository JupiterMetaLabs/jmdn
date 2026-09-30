package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/config/settings"
	seedcommittee "gossipnode/seednode/committee"
)

// Manual sequencer replacement: A is the pinned sequencer, S45 is already
// anchored at block 880, A dies, operators change sequencer_pinned_peer_id to
// B, B (which ran as a validator until now) restarts and takes over.

// 1. The timeout authority (#159) follows the pin: after the change, A can no
// longer drive timeouts and B can.
func TestSequencerReplacement_TimeoutAuthorityFollowsThePin(t *testing.T) {
	privA, idA := libp2pIdentity(t)
	privB, idB := libp2pIdentity(t)
	const chain = 8000800
	reqA, err := SignTimeoutRequest(privA, idA, chain, 905, 0)
	if err != nil {
		t.Fatal(err)
	}
	reqB, err := SignTimeoutRequest(privB, idB, chain, 905, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTimeoutRequest(reqA, idA, chain); err != nil {
		t.Fatalf("before the change A must be accepted: %v", err)
	}
	if err := VerifyTimeoutRequest(reqB, idA, chain); err == nil {
		t.Fatalf("before the change B must be refused")
	}
	// operators set sequencer_pinned_peer_id = B on every node
	if err := VerifyTimeoutRequest(reqB, idB, chain); err != nil {
		t.Fatalf("after the change B must be accepted: %v", err)
	}
	if err := VerifyTimeoutRequest(reqA, idB, chain); err == nil {
		t.Fatalf("after the change a (revived) A must be refused")
	}
}

// 2 + 3. B recovers Period 45's pool from its own chain state, never from the
// seed, and seats exactly the committee A would have seated for 905..919.
func TestSequencerReplacement_BContinuesPeriod45OnTheAnchoredPool(t *testing.T) {
	enableV2(t)
	env := withAnchoring(t, 860)
	s44 := signedSnapshot(t, env.auth, 500, 29, 0)
	s45 := signedSnapshot(t, env.auth, 501, 29, 100)
	b860 := anchorBlockFor(t, 860, 900, s44)
	b880 := anchorBlockFor(t, 880, 930, s45)

	// While A was alive, every node (B included, as a validator) recorded both anchors.
	for _, b := range []*config.ZKBlock{b860, b880} {
		if err := RecordCommitteeAnchor(b); err != nil {
			t.Fatal(err)
		}
	}
	env.mu.Lock()
	env.blocks[860], env.blocks[880] = b860, b880
	env.mu.Unlock()
	env.setTip(904) // A committed up to 904, then died

	seatsByA := map[uint64][]string{}
	for h := uint64(905); h <= 919; h++ {
		m, err := SelectCommittee(RoundContext{SelectionPeriod: 45, EntropyEpoch: 18, PrevHash: []byte{byte(h)}, Height: h})
		if err != nil {
			t.Fatalf("A seating %d: %v", h, err)
		}
		seatsByA[h] = ids(m)
	}

	// B restarts with the new pin: process memory is gone, its DB (KV + blocks) is not.
	anchorMu.Lock()
	anchorRecords = map[uint64]*anchorRecord{}
	anchorMu.Unlock()
	// Meanwhile the seed's membership moved on - B must not pick that up for Period 45.
	setLive(t, 17, 700)
	var fetches atomic.Int32
	SetCommitteeAnchorSource(func(context.Context) (*seedcommittee.CommitteeSnapshot, error) {
		fetches.Add(1)
		return signedSnapshot(t, env.auth, 502, 23, 900), nil
	})

	for h := uint64(905); h <= 919; h++ {
		m, err := SelectCommittee(RoundContext{SelectionPeriod: 45, EntropyEpoch: 18, PrevHash: []byte{byte(h)}, Height: h})
		if err != nil {
			t.Fatalf("B seating %d: %v", h, err)
		}
		if !reflect.DeepEqual(ids(m), seatsByA[h]) {
			t.Fatalf("height %d: B seated a different committee than A would have", h)
		}
	}
	if n := fetches.Load(); n != 0 {
		t.Fatalf("B asked the seed %d times while continuing an already-anchored period", n)
	}
}

// 5. At the next anchor height B fetches the seed's current snapshot, anchors
// it, and every other node (fresh memory, same pin) accepts and records it.
func TestSequencerReplacement_BAnchorsThePeriodAfter(t *testing.T) {
	env := withAnchoring(t, 860)
	s45 := signedSnapshot(t, env.auth, 501, 29, 100)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 880, 930, s45)); err != nil {
		t.Fatal(err)
	}
	s46 := signedSnapshot(t, env.auth, 502, 30, 200)
	SetCommitteeAnchorSource(func(context.Context) (*seedcommittee.CommitteeSnapshot, error) { return s46, nil })

	body, hash, err := BuildCommitteeAnchor(900) // B, as the new proposer
	if err != nil || body == "" {
		t.Fatalf("B could not build the next anchor: %v", err)
	}
	blk := &config.ZKBlock{BlockNumber: 900, Slot: 960, CommitteeSnapshotAnchor: body, CommitteeSnapshotHash: hash}
	blk.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(blk)

	// Another node C: fresh memory, its own KV already holds S45.
	anchorMu.Lock()
	anchorRecords = map[uint64]*anchorRecord{}
	anchorMu.Unlock()
	if rej := checkCommitteeAnchor(blk); rej != nil {
		t.Fatalf("C rejected B's anchor: %+v", rej)
	}
	if err := RecordCommitteeAnchor(blk); err != nil {
		t.Fatal(err)
	}
	pool, handled, err := anchoredPoolForPeriod(46, false)
	if !handled || err != nil || len(pool) != 30 {
		t.Fatalf("period 46 must be B's S46 (30 members), got %d handled=%v err=%v", len(pool), handled, err)
	}

	// And B cannot move membership backwards even if the seed serves an older snapshot.
	SetCommitteeAnchorSource(func(context.Context) (*seedcommittee.CommitteeSnapshot, error) {
		return signedSnapshot(t, env.auth, 499, 10, 0), nil
	})
	_, hash2, err := BuildCommitteeAnchor(920)
	want := AnchorSnapshotHash(s46)
	if err != nil || string(hash2) != string(want[:]) {
		t.Fatalf("an older seed snapshot must be refused and S46 re-carried (err=%v)", err)
	}
	_ = settings.Get()
}

// replacementFixture: A anchored S44 (block 860) and S45 (block 880); B, as a
// validator, recorded both into its own DB. A has committed up to 904 and died.
// The seed's membership has since changed. Counters record every way B could
// reach the seed or its own DB.
type replacementFixture struct {
	env                                           *anchorEnv
	s45                                           *seedcommittee.CommitteeSnapshot
	seatsByA                                      map[uint64][]string
	anchorFetches, liveReads, kvReads, blockReads atomic.Int32
}

func newReplacementFixture(t *testing.T) *replacementFixture {
	t.Helper()
	enableV2(t)
	f := &replacementFixture{env: withAnchoring(t, 860), seatsByA: map[uint64][]string{}}
	env := f.env
	s44 := signedSnapshot(t, env.auth, 500, 29, 0)
	f.s45 = signedSnapshot(t, env.auth, 501, 29, 100)
	b860, b880 := anchorBlockFor(t, 860, 900, s44), anchorBlockFor(t, 880, 930, f.s45)
	for _, b := range []*config.ZKBlock{b860, b880} {
		if err := RecordCommitteeAnchor(b); err != nil {
			t.Fatal(err)
		}
	}
	env.mu.Lock()
	env.blocks[860], env.blocks[880] = b860, b880
	env.mu.Unlock()
	env.setTip(904)
	for h := uint64(905); h <= 919; h++ {
		m, err := SelectCommittee(f.rc(h))
		if err != nil {
			t.Fatal(err)
		}
		f.seatsByA[h] = ids(m)
	}

	// Seed membership changes after S45 was anchored; count every seed touch.
	prevLive := committeeEligibilityFn
	SetCommitteeEligibilitySource(func(uint64, bool) (map[string]string, error) {
		f.liveReads.Add(1)
		out := map[string]string{}
		for i := 0; i < 17; i++ {
			out[fmt.Sprintf("peer-%03d", 700+i)] = fmt.Sprintf("%064x", 700+i+1)
		}
		return out, nil
	})
	t.Cleanup(func() {
		committeeEligibilityMu.Lock()
		committeeEligibilityFn = prevLive
		committeeEligibilityMu.Unlock()
	})
	SetCommitteeAnchorSource(func(context.Context) (*seedcommittee.CommitteeSnapshot, error) {
		f.anchorFetches.Add(1)
		return signedSnapshot(t, env.auth, 777, 23, 900), nil
	})
	innerGet, innerBlock := anchorKVGet, anchorBlockAt
	anchorKVGet = func(p uint64) ([]byte, bool, error) { f.kvReads.Add(1); return innerGet(p) }
	anchorBlockAt = func(h uint64) (*config.ZKBlock, error) { f.blockReads.Add(1); return innerBlock(h) }
	return f
}

func (f *replacementFixture) rc(h uint64) RoundContext {
	return RoundContext{SelectionPeriod: 45, EntropyEpoch: 18, PrevHash: []byte{byte(h)}, Height: h}
}

// B restarts: process memory is gone.
func (f *replacementFixture) restartB() {
	anchorMu.Lock()
	anchorRecords = map[uint64]*anchorRecord{}
	anchorMu.Unlock()
}

func (f *replacementFixture) assertBSeatsS45(t *testing.T) {
	t.Helper()
	for h := uint64(905); h <= 919; h++ {
		m, err := SelectCommittee(f.rc(h))
		if err != nil {
			t.Fatalf("B seating %d: %v", h, err)
		}
		if !reflect.DeepEqual(ids(m), f.seatsByA[h]) {
			t.Fatalf("height %d: B seated a different committee than A", h)
		}
		for _, pid := range ids(m) {
			if strings.HasPrefix(pid, "peer-7") || strings.HasPrefix(pid, "peer-9") {
				t.Fatalf("height %d: B seated %s from the NEW seed membership", h, pid)
			}
		}
	}
}

func (f *replacementFixture) report(t *testing.T, layer string) {
	t.Logf("%-38s seed anchor fetches=%d  seed live reads=%d  KV reads=%d  block reads=%d",
		layer, f.anchorFetches.Load(), f.liveReads.Load(), f.kvReads.Load(), f.blockReads.Load())
	if f.anchorFetches.Load() != 0 || f.liveReads.Load() != 0 {
		t.Fatalf("%s: B touched the seed while recovering an already-anchored period", layer)
	}
}

func TestSequencerReplacement_S45RecoveryLayers(t *testing.T) {
	t.Run("memory intact", func(t *testing.T) {
		f := newReplacementFixture(t)
		f.assertBSeatsS45(t)
		f.report(t, "memory intact")
	})
	t.Run("memory lost, KV committee_anchor:45 present", func(t *testing.T) {
		f := newReplacementFixture(t)
		f.restartB()
		f.assertBSeatsS45(t)
		f.report(t, "memory lost -> KV")
	})
	t.Run("memory and KV lost, anchor block 880 stored", func(t *testing.T) {
		f := newReplacementFixture(t)
		f.restartB()
		f.env.mu.Lock()
		f.env.kv = map[uint64][]byte{}
		f.env.mu.Unlock()
		f.assertBSeatsS45(t)
		f.report(t, "memory+KV lost -> anchor block")
		f.env.mu.Lock()
		_, rewritten := f.env.kv[45]
		f.env.mu.Unlock()
		if !rewritten {
			t.Fatalf("rebuilding from the block must re-persist committee_anchor:45")
		}
	})
	t.Run("KV corrupted, anchor block 880 stored", func(t *testing.T) {
		f := newReplacementFixture(t)
		f.restartB()
		f.env.mu.Lock()
		f.env.kv[45] = []byte(`{"period":45,"snapshot":{"epoch":1}}`)
		f.env.mu.Unlock()
		f.assertBSeatsS45(t)
		f.report(t, "KV corrupted -> anchor block")
	})
	t.Run("nothing provable: memory, KV and block all missing", func(t *testing.T) {
		f := newReplacementFixture(t)
		f.restartB()
		f.env.mu.Lock()
		f.env.kv = map[uint64][]byte{}
		delete(f.env.blocks, 880)
		f.env.mu.Unlock()
		_, err := SelectCommittee(f.rc(905))
		if !errors.Is(err, ErrCommitteeAnchorMissing) {
			t.Fatalf("must fail closed with ErrCommitteeAnchorMissing, got %v", err)
		}
		f.report(t, "nothing provable -> fail closed")
	})
	t.Run("KV holds a different, validly seed-signed snapshot", func(t *testing.T) {
		f := newReplacementFixture(t)
		f.restartB()
		wrong := signedSnapshot(t, f.env.auth, 778, 29, 400) // genuine seed signature, NOT what block 880 anchored
		raw, _ := json.Marshal(anchorRecord{Period: 45, AnchorHeight: 880, AnchorSlot: 930, Snapshot: *wrong})
		f.env.mu.Lock()
		f.env.kv[45] = raw
		f.env.mu.Unlock()
		f.assertBSeatsS45(t)
		f.report(t, "KV substituted -> must use block 880")
	})
}
