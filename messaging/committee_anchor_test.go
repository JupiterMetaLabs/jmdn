package messaging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"sync"
	"testing"

	blssign "gossipnode/AVC/BLS/bls-sign"
	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/config/settings"
	seedcommittee "gossipnode/seednode/committee"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// ---- harness ----------------------------------------------------------------

type testAuthority struct {
	priv   []byte
	pubHex string
}

func newTestAuthority(t *testing.T) testAuthority {
	t.Helper()
	// Fresh random key: GenerateBLSKeyPair would load the node's own persisted
	// key, making every "different" authority the same key. The raw-key helper
	// rejects seeds whose hash exceeds the scalar order, so retry.
	for i := 0; i < 64; i++ {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		if priv, pub, err := blssign.GenerateBLSKeyPairFromRawPrivKey(raw); err == nil {
			return testAuthority{priv: priv, pubHex: hex.EncodeToString(pub)}
		}
	}
	t.Fatal("authority keygen: no valid key in 64 attempts")
	return testAuthority{}
}

// signedSnapshot builds a seed-style snapshot of n members (peer-000..) signed
// by auth for epoch. offset shifts the member ids so two pools can differ.
func signedSnapshot(t *testing.T, auth testAuthority, epoch uint64, n, offset int) *seedcommittee.CommitteeSnapshot {
	t.Helper()
	entries := make([]seedcommittee.CommitteeEntry, 0, n)
	for i := 0; i < n; i++ {
		id := offset + i
		entries = append(entries, seedcommittee.CommitteeEntry{
			PeerID:        fmt.Sprintf("peer-%03d", id),
			BLSPub:        fmt.Sprintf("%064x", id+1),
			RewardAddress: fmt.Sprintf("0x%040x", id+1),
		})
	}
	sig, err := blssign.BLSSign(auth.priv, seedcommittee.CanonicalCommitteeBytes(epoch, "", entries))
	if err != nil {
		t.Fatalf("sign snapshot: %v", err)
	}
	return &seedcommittee.CommitteeSnapshot{
		Epoch: epoch, Entries: entries, AuthorityPubHex: auth.pubHex, Signature: hex.EncodeToString(sig),
	}
}

func anchorBlockFor(t *testing.T, height, slot uint64, snap *seedcommittee.CommitteeSnapshot) *config.ZKBlock {
	t.Helper()
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	h := AnchorSnapshotHash(snap)
	b := &config.ZKBlock{BlockNumber: height, Slot: slot, CommitteeSnapshotAnchor: string(body), CommitteeSnapshotHash: h[:]}
	b.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(b) // as the proposer does, last
	return b
}

// anchorEnv turns anchoring on (activation, L=20, pinned authority), gives the
// store fresh in-memory persistence and a controllable committed tip, and
// restores everything afterwards.
type anchorEnv struct {
	mu       sync.Mutex
	kv       map[uint64][]byte
	blocks   map[uint64]*config.ZKBlock
	tip      uint64
	nextSlot uint64
	auth     testAuthority
	kvPuts   int
	blockFn  func(uint64) (*config.ZKBlock, error)
}

func withAnchoring(t *testing.T, activation uint64) *anchorEnv {
	t.Helper()
	cfg := settings.Get()
	prevAct, prevL, prevPin := cfg.Consensus.CommitteeAnchorActivationHeight, cfg.Consensus.CommitteeEpochBlocks, cfg.Consensus.SeedAuthorityBLSPub
	env := &anchorEnv{kv: map[uint64][]byte{}, blocks: map[uint64]*config.ZKBlock{}, auth: newTestAuthority(t)}
	cfg.Consensus.CommitteeAnchorActivationHeight = activation
	cfg.Consensus.CommitteeEpochBlocks = 20
	cfg.Consensus.SeedAuthorityBLSPub = env.auth.pubHex

	prevGet, prevPut, prevBlock, prevTip := anchorKVGet, anchorKVPut, anchorBlockAt, committedTipFn
	prevNextSlot := nextPossibleSlotFn
	env.nextSlot = ^uint64(0) // "every cutoff is final" unless a test says otherwise
	nextPossibleSlotFn = func(uint64) uint64 { env.mu.Lock(); defer env.mu.Unlock(); return env.nextSlot }
	anchorMu.Lock()
	prevRecords := anchorRecords
	anchorRecords = map[uint64]*anchorRecord{}
	anchorMu.Unlock()
	anchorKVGet = func(p uint64) ([]byte, bool, error) {
		env.mu.Lock()
		defer env.mu.Unlock()
		raw, ok := env.kv[p]
		return raw, ok, nil
	}
	anchorKVPut = func(p uint64, raw []byte) error {
		env.mu.Lock()
		defer env.mu.Unlock()
		env.kvPuts++
		if _, ok := env.kv[p]; !ok {
			env.kv[p] = raw
		}
		return nil
	}
	anchorBlockAt = func(h uint64) (*config.ZKBlock, error) {
		env.mu.Lock()
		defer env.mu.Unlock()
		if b, ok := env.blocks[h]; ok {
			return b, nil
		}
		return nil, errors.New("not stored")
	}
	committedTipFn = func() uint64 { env.mu.Lock(); defer env.mu.Unlock(); return env.tip }

	t.Cleanup(func() {
		cfg.Consensus.CommitteeAnchorActivationHeight = prevAct
		cfg.Consensus.CommitteeEpochBlocks = prevL
		cfg.Consensus.SeedAuthorityBLSPub = prevPin
		anchorKVGet, anchorKVPut, anchorBlockAt, committedTipFn = prevGet, prevPut, prevBlock, prevTip
		nextPossibleSlotFn = prevNextSlot
		anchorMu.Lock()
		anchorRecords = prevRecords
		anchorMu.Unlock()
		SetCommitteeAnchorSource(nil)
	})
	return env
}

func (e *anchorEnv) setTip(h uint64) { e.mu.Lock(); e.tip = h; e.mu.Unlock() }

func (e *anchorEnv) setNextSlot(s uint64) { e.mu.Lock(); e.nextSlot = s; e.mu.Unlock() }

// setLive installs a live seed source returning n members at offset - the
// thing that used to decide the pool and must no longer matter once anchored.
func setLive(t *testing.T, n, offset int) {
	t.Helper()
	prev := committeeEligibilityFn
	SetCommitteeEligibilitySource(func(_ uint64, _ bool) (map[string]string, error) {
		out := make(map[string]string, n)
		for i := 0; i < n; i++ {
			out[fmt.Sprintf("peer-%03d", offset+i)] = fmt.Sprintf("%064x", offset+i+1)
		}
		return out, nil
	})
	t.Cleanup(func() {
		committeeEligibilityMu.Lock()
		committeeEligibilityFn = prev
		committeeEligibilityMu.Unlock()
	})
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- schedule ---------------------------------------------------------------

func TestCommitteeAnchor_Schedule(t *testing.T) {
	withAnchoring(t, 845) // rounds up to 860
	if got := firstCommitteeAnchorHeight(); got != 860 {
		t.Fatalf("first anchor height = %d, want 860 (845 rounded up to a multiple of 20)", got)
	}
	for h, want := range map[uint64]bool{840: false, 859: false, 860: true, 861: false, 880: true, 1000: true} {
		if IsCommitteeAnchorHeight(h) != want {
			t.Fatalf("IsCommitteeAnchorHeight(%d) = %v, want %v", h, !want, want)
		}
	}
	// The anchor at 860 (period 43) defines period 44; period 43 is still legacy.
	if periodIsAnchored(43) || !periodIsAnchored(44) {
		t.Fatalf("period 43 must be legacy and 44 anchored")
	}
}

func TestCommitteeAnchor_OffByDefaultIsInert(t *testing.T) {
	env := withAnchoring(t, 0)
	_ = env
	if CommitteeAnchoringEnabled() || IsCommitteeAnchorHeight(860) {
		t.Fatalf("activation 0 must disable anchoring")
	}
	if rej := checkCommitteeAnchor(&config.ZKBlock{BlockNumber: 860}); rej != nil {
		t.Fatalf("an ordinary block must pass when anchoring is off: %+v", rej)
	}
	// Rollback: anchor blocks already in the chain must still be accepted after
	// the fleet sets the activation height back to 0.
	withBody := &config.ZKBlock{BlockNumber: 860, CommitteeSnapshotAnchor: `{"epoch":1}`, CommitteeSnapshotHash: []byte{1}}
	if rej := checkCommitteeAnchor(withBody); rej != nil {
		t.Fatalf("anchoring off must ignore an existing anchor body (rollback), got %+v", rej)
	}
	if err := RecordCommitteeAnchor(withBody); err != nil {
		t.Fatalf("anchoring off must not record anything: %v", err)
	}
	if body, hash, err := BuildCommitteeAnchor(860); body != "" || hash != nil || err != nil {
		t.Fatalf("no anchor may be built when off")
	}
}

// ---- verification -----------------------------------------------------------

func TestCheckCommitteeAnchor_AcceptsAValidAnchor(t *testing.T) {
	env := withAnchoring(t, 860)
	if rej := checkCommitteeAnchor(anchorBlockFor(t, 860, 900, signedSnapshot(t, env.auth, 500, 29, 0))); rej != nil {
		t.Fatalf("valid anchor rejected: %+v", rej)
	}
}

func TestCheckCommitteeAnchor_Rejections(t *testing.T) {
	env := withAnchoring(t, 860)
	good := signedSnapshot(t, env.auth, 500, 29, 0)

	tampered := anchorBlockFor(t, 860, 900, good)
	var s seedcommittee.CommitteeSnapshot
	_ = json.Unmarshal([]byte(tampered.CommitteeSnapshotAnchor), &s)
	s.Entries[3].PeerID = "attacker"
	raw, _ := json.Marshal(s)
	tampered.CommitteeSnapshotAnchor = string(raw)

	other := newTestAuthority(t)
	foreign := anchorBlockFor(t, 860, 900, signedSnapshot(t, other, 500, 29, 0))

	hashOnly := anchorBlockFor(t, 860, 900, good)
	hashOnly.CommitteeSnapshotAnchor = ""

	wrongHash := anchorBlockFor(t, 860, 900, good)
	wrongHash.CommitteeSnapshotHash = make([]byte, 32)

	nonAnchor := anchorBlockFor(t, 861, 901, good)
	strayHash := &config.ZKBlock{BlockNumber: 862, CommitteeSnapshotHash: []byte{1}}
	preActivation := anchorBlockFor(t, 840, 880, good)

	for name, b := range map[string]*config.ZKBlock{
		"tampered member list":         tampered,
		"signed by a non-pinned key":   foreign,
		"anchor height without body":   hashOnly,
		"hash does not match body":     wrongHash,
		"body on a non-anchor height":  nonAnchor,
		"anchor hash on a non-anchor":  strayHash,
		"body below activation height": preActivation,
	} {
		if rej := checkCommitteeAnchor(b); rej == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
	if rej := checkCommitteeAnchor(&config.ZKBlock{BlockNumber: 840}); rej != nil {
		t.Fatalf("an ordinary pre-activation block must pass: %+v", rej)
	}
	if rej := checkCommitteeAnchor(&config.ZKBlock{BlockNumber: 861}); rej != nil {
		t.Fatalf("an ordinary post-activation non-anchor block must pass: %+v", rej)
	}
}

func TestCheckCommitteeAnchor_EpochMayRepeatButNeverRegress(t *testing.T) {
	env := withAnchoring(t, 860)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 900, signedSnapshot(t, env.auth, 500, 29, 0))); err != nil {
		t.Fatal(err)
	}
	if rej := checkCommitteeAnchor(anchorBlockFor(t, 880, 920, signedSnapshot(t, env.auth, 499, 29, 0))); rej == nil || rej.reason != "committee_anchor_regressed" {
		t.Fatalf("an older membership epoch must be rejected as regressed, got %+v", rej)
	}
	if rej := checkCommitteeAnchor(anchorBlockFor(t, 880, 920, signedSnapshot(t, env.auth, 500, 29, 0))); rej != nil {
		t.Fatalf("re-carrying the same epoch (seed unreachable) must pass: %+v", rej)
	}
}

// ---- the W1 property: one pool, every node, every consumer ---------------------

// TestAnchoredPool_EveryConsumerAgreesWhateverTheSeedSays is the defect this
// fixes. The live seed source changes (26 -> 30 members, a membership change or
// hour boundary) while a period is in progress. Before anchoring, every consumer
// followed it. After, the seats, the verifier's pool, the buddy tally, the
// timeout pool, the fleet snapshot and the reward map all stay on the pool the
// chain anchored - on every node, whenever it asks.
func TestAnchoredPool_EveryConsumerAgreesWhateverTheSeedSays(t *testing.T) {
	enableV2(t)
	env := withAnchoring(t, 860)
	anchored := signedSnapshot(t, env.auth, 500, 29, 100) // peer-100..128
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 900, anchored)); err != nil {
		t.Fatal(err)
	}
	env.setTip(889) // deciding height 890, period 44

	wantPool := map[string]string{}
	for _, e := range anchored.Entries {
		wantPool[e.PeerID] = normalizeBLSPub(e.BLSPub)
	}
	rc := RoundContext{SelectionPeriod: 44, EntropyEpoch: 17, PrevHash: []byte("parent-889"), Height: 890}

	var seats [][]string
	for _, live := range []struct{ n, off int }{{26, 0}, {30, 500}} {
		setLive(t, live.n, live.off)

		members, err := SelectCommittee(rc)
		if err != nil {
			t.Fatalf("SelectCommittee: %v", err)
		}
		seats = append(seats, ids(members))

		cur, err := eligibleMembersUncapped()
		if err != nil || !reflect.DeepEqual(cur, wantPool) {
			t.Fatalf("current pool must be the anchored pool (err=%v)", err)
		}
		tally, err := AuthorizedCommitteeForTallyAtHeight(890)
		if err != nil || !reflect.DeepEqual(tally, wantPool) {
			t.Fatalf("buddy tally must use the anchored pool (err=%v)", err)
		}
		size, _, err := timeoutVotingPool(890)
		if err != nil || size != 29 {
			t.Fatalf("timeout pool size = %d (err=%v), want 29", size, err)
		}
		fleet, err := fleetCommitteeSnapshotFor(44)
		if err != nil || len(fleet.Members) != 29 {
			t.Fatalf("fleet snapshot must hold the 29 anchored members (err=%v)", err)
		}
		for _, m := range members {
			if _, ok := wantPool[m.PeerID]; !ok {
				t.Fatalf("seated %s, which is not in the anchored pool", m.PeerID)
			}
		}
	}
	if !reflect.DeepEqual(seats[0], seats[1]) {
		t.Fatalf("seats changed when the live seed source changed:\n%v\n%v", seats[0], seats[1])
	}

	// And they are exactly the pure draw over the anchored pool.
	snap := snapshotFromEligible(44, wantPool)
	src, _ := SeedSourceFor(rc.EntropyEpoch)
	seed, _ := committee.DeriveSeed(src, committee.SeedInput{EntropyEpoch: rc.EntropyEpoch, PrevHash: rc.PrevHash, Height: rc.Height, Period: rc.Period})
	want, err := committee.CommitteeFor(seed, snap, committeeSizeLimit())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seats[0], ids(want)) {
		t.Fatalf("seats are not the draw over the anchored pool")
	}
}

func TestAnchoredPool_TwoNodesAtDifferentTimesSeatTheSameCommittee(t *testing.T) {
	enableV2(t)
	env := withAnchoring(t, 860)
	block := anchorBlockFor(t, 860, 900, signedSnapshot(t, env.auth, 500, 29, 0))
	rc := RoundContext{SelectionPeriod: 44, EntropyEpoch: 17, PrevHash: []byte("p"), Height: 895}

	// Node A records the anchor from gossip while the seed says 26 members.
	setLive(t, 26, 0)
	if err := RecordCommitteeAnchor(block); err != nil {
		t.Fatal(err)
	}
	env.setTip(894)
	a, err := SelectCommittee(rc)
	if err != nil {
		t.Fatal(err)
	}

	// Node B restarts later (memory empty), the seed now says 31, and it only
	// has the anchor block in its own DB.
	anchorMu.Lock()
	anchorRecords = map[uint64]*anchorRecord{}
	anchorMu.Unlock()
	env.mu.Lock()
	env.kv = map[uint64][]byte{}
	env.blocks[860] = block
	env.mu.Unlock()
	setLive(t, 31, 700)
	b, err := SelectCommittee(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(a), ids(b)) {
		t.Fatalf("node A and node B seated different committees for the same block")
	}
}

func TestAnchoredPool_MissingAnchorFailsClosed(t *testing.T) {
	enableV2(t)
	withAnchoring(t, 860)
	setLive(t, 29, 0) // a live source exists and must NOT be used
	_, err := SelectCommittee(RoundContext{SelectionPeriod: 44, EntropyEpoch: 17, PrevHash: []byte("p"), Height: 890})
	if !errors.Is(err, ErrCommitteeAnchorMissing) {
		t.Fatalf("an anchored period with no provable pool must fail closed, got %v", err)
	}
}

func TestAnchoredPool_LegacyPeriodsAreUnchanged(t *testing.T) {
	enableV2(t)
	env := withAnchoring(t, 860)
	env.setTip(870) // deciding 871: period 43, before the first anchored period
	setLive(t, 12, 0)
	pool, err := eligibleMembersUncapped()
	if err != nil || len(pool) != 12 {
		t.Fatalf("a legacy period must still read the live source (got %d, err=%v)", len(pool), err)
	}
}

func TestAnchoredRecord_RestoredFromDurableStore(t *testing.T) {
	env := withAnchoring(t, 860)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 900, signedSnapshot(t, env.auth, 500, 29, 0))); err != nil {
		t.Fatal(err)
	}
	if env.kvPuts != 1 {
		t.Fatalf("recording must persist once, got %d writes", env.kvPuts)
	}
	anchorMu.Lock()
	anchorRecords = map[uint64]*anchorRecord{} // restart: memory gone
	anchorMu.Unlock()
	pool, handled, err := anchoredPoolForPeriod(44, false)
	if !handled || err != nil || len(pool) != 29 {
		t.Fatalf("pool must come back from the durable record (handled=%v err=%v len=%d)", handled, err, len(pool))
	}
}

func TestAnchoredPool_BlocklistOnlyFiltersTheLocalView(t *testing.T) {
	env := withAnchoring(t, 860)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 900, signedSnapshot(t, env.auth, 500, 29, 0))); err != nil {
		t.Fatal(err)
	}
	cfg := settings.Get()
	prev := cfg.Consensus.BlockBuddy
	cfg.Consensus.BlockBuddy = []string{"peer-005"}
	t.Cleanup(func() { cfg.Consensus.BlockBuddy = prev })

	local, _, _ := anchoredPoolForPeriod(44, true)
	fleet, _, _ := anchoredPoolForPeriod(44, false)
	if _, ok := local["peer-005"]; ok || len(local) != 28 {
		t.Fatalf("blocklisted peer must be removed from the local view")
	}
	if _, ok := fleet["peer-005"]; !ok || len(fleet) != 29 {
		t.Fatalf("the fleet-agreed pool (sizes n) must ignore the local blocklist")
	}
}

func TestRewardAddresses_ComeFromTheAnchoredSnapshot(t *testing.T) {
	env := withAnchoring(t, 860)
	snap := signedSnapshot(t, env.auth, 500, 29, 0)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 900, snap)); err != nil {
		t.Fatal(err)
	}
	prev := rewardAddrSource
	SetRewardAddressSource(func() (map[string]string, error) { return map[string]string{"peer-999": "0xdead"}, nil })
	t.Cleanup(func() { SetRewardAddressSource(prev) })

	// Block 891 pays the certifiers of 890 (period 44): the anchored snapshot.
	got, err := rewardAddressesForHeight(891)
	if err != nil || !reflect.DeepEqual(got, snap.RewardAddrByPeer()) {
		t.Fatalf("reward map must be the anchored snapshot's (err=%v)", err)
	}
	// Block 870 pays 869 (period 43, legacy): the live source.
	got, err = rewardAddressesForHeight(870)
	if err != nil || got["peer-999"] != "0xdead" {
		t.Fatalf("a legacy period must keep the live reward source (err=%v)", err)
	}
}

func TestEntropyPool_IsTheNewestAnchorBeforeTheCutoff(t *testing.T) {
	env := withAnchoring(t, 860)
	a860 := signedSnapshot(t, env.auth, 500, 29, 0)
	a880 := signedSnapshot(t, env.auth, 501, 29, 200)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 1000, a860)); err != nil {
		t.Fatal(err)
	}
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 880, 1030, a880)); err != nil {
		t.Fatal(err)
	}
	env.setTip(899)

	// Epoch 21 cutoff = 21*50 - K. K = SnapshotFreezeLookahead.
	cutoff := 21*N - SnapshotFreezeLookahead
	pool, handled, err := entropyAnchoredPool(21)
	if !handled || err != nil {
		t.Fatalf("entropy pool: handled=%v err=%v", handled, err)
	}
	wantOffset := 0
	if 1030 <= cutoff {
		wantOffset = 200
	}
	if _, ok := pool[fmt.Sprintf("peer-%03d", wantOffset)]; !ok {
		t.Fatalf("entropy pool must be the newest anchor at or before cutoff slot %d", cutoff)
	}

	// Same answer regardless of the live seed source.
	setLive(t, 5, 900)
	again, _, _ := entropyAnchoredPool(21)
	if !reflect.DeepEqual(sortedKeys(pool), sortedKeys(again)) {
		t.Fatalf("entropy pool changed with the live source")
	}

	// An epoch whose cutoff precedes every anchor keeps the pre-W1 pool.
	if _, handled, _ := entropyAnchoredPool(1); handled {
		t.Fatalf("an epoch before any anchor must not be anchor-resolved")
	}
}

// ---- proposer ---------------------------------------------------------------

func TestBuildCommitteeAnchor_CarriesTheSeedSnapshotAndPassesVerification(t *testing.T) {
	env := withAnchoring(t, 860)
	snap := signedSnapshot(t, env.auth, 500, 29, 0)
	SetCommitteeAnchorSource(func(context.Context) (*seedcommittee.CommitteeSnapshot, error) { return snap, nil })

	body, hash, err := BuildCommitteeAnchor(860)
	if err != nil || body == "" {
		t.Fatalf("anchor build failed: %v", err)
	}
	blk := &config.ZKBlock{BlockNumber: 860, CommitteeSnapshotAnchor: body, CommitteeSnapshotHash: hash}
	blk.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(blk)
	if rej := checkCommitteeAnchor(blk); rej != nil {
		t.Fatalf("a proposer-built anchor must pass the receiver check: %+v", rej)
	}
	if b, h, err := BuildCommitteeAnchor(861); b != "" || h != nil || err != nil {
		t.Fatalf("non-anchor heights carry nothing")
	}
}

func TestBuildCommitteeAnchor_ReCarriesThePreviousPoolWhenTheSeedFails(t *testing.T) {
	env := withAnchoring(t, 860)
	first := signedSnapshot(t, env.auth, 500, 29, 0)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 900, first)); err != nil {
		t.Fatal(err)
	}
	wantHash := AnchorSnapshotHash(first)

	for name, src := range map[string]func(context.Context) (*seedcommittee.CommitteeSnapshot, error){
		"seed unreachable": func(context.Context) (*seedcommittee.CommitteeSnapshot, error) { return nil, errors.New("down") },
		"seed older epoch": func(context.Context) (*seedcommittee.CommitteeSnapshot, error) {
			return signedSnapshot(t, env.auth, 499, 30, 0), nil
		},
		"seed bad signature": func(context.Context) (*seedcommittee.CommitteeSnapshot, error) {
			return signedSnapshot(t, newTestAuthority(t), 600, 30, 0), nil
		},
	} {
		SetCommitteeAnchorSource(src)
		_, hash, err := BuildCommitteeAnchor(880)
		if err != nil || string(hash) != string(wantHash[:]) {
			t.Fatalf("%s: must re-carry the previous pool (err=%v)", name, err)
		}
	}
}

func TestBuildCommitteeAnchor_FirstAnchorWithoutSeedFailsLoudly(t *testing.T) {
	withAnchoring(t, 860)
	SetCommitteeAnchorSource(func(context.Context) (*seedcommittee.CommitteeSnapshot, error) { return nil, errors.New("down") })
	if _, _, err := BuildCommitteeAnchor(860); err == nil {
		t.Fatalf("the first anchor has nothing to fall back to and must refuse")
	}
}

// ---- config -----------------------------------------------------------------

func TestValidateCommitteeAnchorConfig(t *testing.T) {
	withAnchoring(t, 860)
	if err := ValidateCommitteeAnchorConfig(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cfg := settings.Get()

	pin := cfg.Consensus.SeedAuthorityBLSPub
	cfg.Consensus.SeedAuthorityBLSPub = ""
	if ValidateCommitteeAnchorConfig() == nil {
		t.Errorf("anchoring without a pinned seed authority must be refused")
	}
	cfg.Consensus.SeedAuthorityBLSPub = pin

	cfg.Consensus.CommitteeEpochBlocks = 0
	if ValidateCommitteeAnchorConfig() == nil {
		t.Errorf("anchoring with committee_epoch_blocks = 0 must be refused")
	}
	cfg.Consensus.CommitteeEpochBlocks = 20

	prev := CommitteeSnapshotAnchorEnabled
	CommitteeSnapshotAnchorEnabled = true
	if ValidateCommitteeAnchorConfig() == nil {
		t.Errorf("both anchors on must be refused")
	}
	CommitteeSnapshotAnchorEnabled = prev
}

// TestAnchoredPool_HeightScopedConsumersFollowTheHeightNotTheTip covers a node
// whose committed tip is one period behind the height it is working on (a
// lagging buddy, a node catching up, a timeout for the first block of a new
// period). The tally, the timeout quorum and the fleet snapshot must use the
// pool anchored for THAT height's period, not the pool current at the tip.
func TestAnchoredPool_HeightScopedConsumersFollowTheHeightNotTheTip(t *testing.T) {
	enableV2(t)
	env := withAnchoring(t, 860)
	a := signedSnapshot(t, env.auth, 500, 29, 0)   // defines period 44 (880-899)
	b := signedSnapshot(t, env.auth, 501, 23, 300) // defines period 45 (900-919)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 900, a)); err != nil {
		t.Fatal(err)
	}
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 880, 930, b)); err != nil {
		t.Fatal(err)
	}
	env.setTip(885) // current pool = period 44 = a
	setLive(t, 17, 800)

	if cur, _ := eligibleMembersUncapped(); len(cur) != 29 {
		t.Fatalf("precondition: current pool must be period 44's (29), got %d", len(cur))
	}
	tally, err := AuthorizedCommitteeForTallyAtHeight(905)
	if err != nil || len(tally) != 23 {
		t.Fatalf("tally for height 905 must use period 45's anchored pool (23), got %d err=%v", len(tally), err)
	}
	size, _, err := timeoutVotingPool(905)
	if err != nil || size != 23 {
		t.Fatalf("timeout quorum for height 905 must be sized on period 45's pool (23), got %d err=%v", size, err)
	}
	fleet, err := fleetCommitteeSnapshotFor(45)
	if err != nil || len(fleet.Members) != 23 {
		t.Fatalf("fleet snapshot for period 45 must be its anchored pool (23), got %d err=%v", len(fleet.Members), err)
	}
}

// TestCheckCommitteeAnchor_SnapshotMustBeTheOneTheCommitteeSigned is the
// substitution attack: a relay or sync peer replaces the anchor with a
// DIFFERENT snapshot that the seed really did sign (and a matching
// CommitteeSnapshotHash), leaving the certified ConsensusHash alone. The seed
// signature and the hash both check out; only the ConsensusHash recompute
// notices. Also: an anchor block with no ConsensusHash at all is refused.
func TestCheckCommitteeAnchor_SnapshotMustBeTheOneTheCommitteeSigned(t *testing.T) {
	env := withAnchoring(t, 860)
	honest := anchorBlockFor(t, 860, 900, signedSnapshot(t, env.auth, 500, 29, 0))

	other := signedSnapshot(t, env.auth, 501, 29, 400) // genuine seed snapshot, different members
	body, _ := json.Marshal(other)
	h := AnchorSnapshotHash(other)
	swapped := *honest
	swapped.CommitteeSnapshotAnchor = string(body)
	swapped.CommitteeSnapshotHash = h[:]
	if _, err := parseAndVerifyAnchor(swapped.CommitteeSnapshotAnchor, swapped.CommitteeSnapshotHash); err != nil {
		t.Fatalf("precondition: the swapped snapshot must pass seed + hash checks on its own: %v", err)
	}
	if rej := checkCommitteeAnchor(&swapped); rej == nil || rej.reason != "committee_anchor_unbound" {
		t.Fatalf("a substituted snapshot must be rejected as unbound, got %+v", rej)
	}
	if err := ValidateCommitteeAnchor(&swapped); err == nil {
		t.Fatalf("the sync path (ValidateCommitteeAnchor) must reject the substitution too")
	}
	if err := RecordCommitteeAnchor(&swapped); err == nil {
		t.Fatalf("RecordCommitteeAnchor must never store a substituted snapshot")
	}

	zero := *honest
	zero.ConsensusHash = common.Hash{}
	if rej := checkCommitteeAnchor(&zero); rej == nil || rej.reason != "committee_anchor_unbound" {
		t.Fatalf("an anchor block without ConsensusHash must be rejected, got %+v", rej)
	}
	if rej := checkCommitteeAnchor(honest); rej != nil {
		t.Fatalf("the honest anchor must still pass: %+v", rej)
	}
}

// An anchor block that contains an EIP-1559 transaction, read back from the DB
// (ChainID dropped), must still bind: this is the ThebeSync / restart shape. A
// contents-based recompute here would halt every syncing node at the anchor.
func TestCheckCommitteeAnchor_BindsOnTheStoredShapeWithTypedTransactions(t *testing.T) {
	env := withAnchoring(t, 860)
	chain := big.NewInt(7000700)
	key, _ := ethcrypto.GenerateKey()
	to := ethcommonAddr()
	stx, err := ethtypes.SignNewTx(key, ethtypes.LatestSignerForChainID(chain), &ethtypes.DynamicFeeTx{
		ChainID: chain, Nonce: 1, To: &to, Value: big.NewInt(1), Gas: 21000, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2)})
	if err != nil {
		t.Fatal(err)
	}
	v, r, s := stx.RawSignatureValues()
	tx := config.Transaction{Hash: stx.Hash(), Type: 2, ChainID: chain, Nonce: 1, To: &to, Value: big.NewInt(1),
		GasLimit: 21000, MaxFee: big.NewInt(2), MaxPriorityFee: big.NewInt(1), V: v, R: r, S: s}

	snap := signedSnapshot(t, env.auth, 500, 29, 0)
	body, _ := json.Marshal(snap)
	h := AnchorSnapshotHash(snap)
	live := &config.ZKBlock{BlockNumber: 860, Slot: 900, Transactions: []config.Transaction{tx},
		CommitteeSnapshotAnchor: string(body), CommitteeSnapshotHash: h[:]}
	live.ConsensusHash = Security.RecomputeBlockHashWithConsensusFields(live) // the proposer's contents-based value

	stored := *live
	stored.Transactions = []config.Transaction{tx}
	stored.Transactions[0].ChainID = nil
	if Security.RecomputeBlockHashWithConsensusFields(&stored) == live.ConsensusHash {
		t.Fatal("precondition: the stored shape must break the contents-based recompute")
	}
	for name, b := range map[string]*config.ZKBlock{"live": live, "stored": &stored} {
		if rej := checkCommitteeAnchor(b); rej != nil {
			t.Fatalf("%s shape: honest anchor rejected: %+v", name, rej)
		}
	}
	if err := RecordCommitteeAnchor(&stored); err != nil {
		t.Fatalf("rebuilding from the stored anchor block must work: %v", err)
	}
}

func ethcommonAddr() common.Address {
	return common.HexToAddress("0x2222222222222222222222222222222222222222")
}

// The body must hash to the certified CommitteeSnapshotHash even when the
// ConsensusHash is internally consistent: the certificate covers the hash, not
// the body, so a relay that swaps only the body (for another validly
// seed-signed snapshot) must be caught by the body->hash check.
func TestCheckCommitteeAnchor_BodyMustMatchTheCertifiedHash(t *testing.T) {
	env := withAnchoring(t, 860)
	certified := anchorBlockFor(t, 860, 900, signedSnapshot(t, env.auth, 500, 29, 0))
	other, _ := json.Marshal(signedSnapshot(t, env.auth, 501, 29, 400))
	swapped := *certified
	swapped.CommitteeSnapshotAnchor = string(other) // hash and ConsensusHash untouched
	if err := anchorBindingError(&swapped); err != nil {
		t.Fatalf("precondition: the ConsensusHash binding alone must pass (the body is not in it): %v", err)
	}
	if rej := checkCommitteeAnchor(&swapped); rej == nil || rej.reason != "committee_anchor_invalid" {
		t.Fatalf("a body that does not hash to the certified CommitteeSnapshotHash must be rejected, got %+v", rej)
	}
}

func TestPreflightCommitteeAnchorSource(t *testing.T) {
	env := withAnchoring(t, 860)
	good := signedSnapshot(t, env.auth, 500, 29, 0)

	SetCommitteeAnchorSource(nil)
	if _, _, err := PreflightCommitteeAnchorSource(); err == nil {
		t.Errorf("no source wired must be reported")
	}
	SetCommitteeAnchorSource(func(context.Context) (*seedcommittee.CommitteeSnapshot, error) { return nil, errors.New("seed down") })
	if _, _, err := PreflightCommitteeAnchorSource(); err == nil {
		t.Errorf("an unreachable seed must be reported")
	}
	SetCommitteeAnchorSource(func(context.Context) (*seedcommittee.CommitteeSnapshot, error) {
		return signedSnapshot(t, newTestAuthority(t), 500, 29, 0), nil
	})
	if _, _, err := PreflightCommitteeAnchorSource(); err == nil {
		t.Errorf("a snapshot signed by a key other than the pinned one must be reported")
	}
	SetCommitteeAnchorSource(func(context.Context) (*seedcommittee.CommitteeSnapshot, error) { return good, nil })
	if epoch, n, err := PreflightCommitteeAnchorSource(); err != nil || epoch != 500 || n != 29 {
		t.Errorf("valid source: epoch=%d members=%d err=%v", epoch, n, err)
	}

	settings.Get().Consensus.CommitteeAnchorActivationHeight = 0
	SetCommitteeAnchorSource(nil)
	if _, _, err := PreflightCommitteeAnchorSource(); err != nil {
		t.Errorf("anchoring off must be a silent no-op, got %v", err)
	}
}
