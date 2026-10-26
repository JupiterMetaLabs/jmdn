package messaging

// Tests for entropy_reveal.go and entropy_reveal_produce.go — Stage B/C under
// Architecture §4.3 Decision A, with BLS reveals (2026-10-22 audit, A-2).
//
// There is no commit phase to simulate: a reveal is a deterministic BLS
// signature under the member's committee key, so these tests exercise the real
// production path end to end. The committee comes from revealKeyring
// (entropy_reveal_keys_test.go), which gives every member a real BLS key.

import (
	"errors"
	"fmt"
	"testing"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/JupiterMetaLabs/avc/randao"

	"gossipnode/config"
)

// resetEntropyAccumulatorStore swaps in a fresh store for the test's duration
// and restores the original afterward — this is a process-wide singleton other
// tests may also touch.
func resetEntropyAccumulatorStore(t *testing.T) {
	t.Helper()
	saved := defaultEntropyAccumulatorStore
	defaultEntropyAccumulatorStore = &entropyAccumulatorStore{
		accs: make(map[uint64]*randao.Accumulator),
	}
	t.Cleanup(func() { defaultEntropyAccumulatorStore = saved })
}

// newTestIdentity returns a real libp2p identity — the same kind node/node.go
// creates (an Ed25519 node key). SetNodeIdentity checks the key derives the
// peer ID, so tests that install a node identity need a genuine pair. It has
// nothing to do with the reveal, which is a BLS signature.
func newTestIdentity(t *testing.T) (ic.PrivKey, string) {
	t.Helper()
	priv, _, err := ic.GenerateKeyPair(ic.Ed25519, 0)
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	pid, err := peer.IDFromPublicKey(priv.GetPublic())
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	return priv, pid.String()
}

// wireEligibilityWithPeers is wireEligibility with caller-supplied peer IDs.
func wireEligibilityWithPeers(t *testing.T, peerIDs []string) {
	t.Helper()
	SetCommitteeEligibilitySource(func(_ uint64, _ bool) (map[string]string, error) {
		out := make(map[string]string, len(peerIDs))
		for i, id := range peerIDs {
			out[id] = fmt.Sprintf("%064x", i)
		}
		return out, nil
	})
	t.Cleanup(func() {
		SetCommitteeEligibilitySource(defaultTestEligibility)
		beaconSource = nil
	})
}

// withNodeIdentity installs a node identity for the test and restores the
// previous one afterward.
func withNodeIdentity(t *testing.T, priv ic.PrivKey, peerID string) {
	t.Helper()
	nodeIdentityMu.Lock()
	savedPriv, savedPeer := nodeIdentityPriv, nodeIdentityPeer
	nodeIdentityMu.Unlock()

	if err := SetNodeIdentity(priv, peerID); err != nil {
		t.Fatalf("SetNodeIdentity: %v", err)
	}
	t.Cleanup(func() {
		nodeIdentityMu.Lock()
		nodeIdentityPriv, nodeIdentityPeer = savedPriv, savedPeer
		nodeIdentityMu.Unlock()
	})
}

// --- foldBlockDeclaredReveals: no-op / fail-closed paths --------------------

func TestFoldBlockDeclaredReveals_EmptyReveals_IsNoOp(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	beaconSource = nil // if this were reached it would error

	foldBlockDeclaredReveals(&config.ZKBlock{BlockNumber: 1, Slot: 10}) // must not panic

	if len(defaultEntropyAccumulatorStore.accs) != 0 {
		t.Fatal("an Accumulator was constructed for a block with no declared reveals — this is the common case and must stay a true no-op")
	}
}

func TestFoldBlockDeclaredReveals_NoBeaconInstalled_LogsAndDoesNotPanic(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	beaconSource = nil
	pruneBLSRevealCommitteesBelow(^uint64(0))

	block := &config.ZKBlock{
		BlockNumber:   1,
		Slot:          10,
		RandaoReveals: []config.Reveal{{ProposerID: "v1", Secret: make([]byte, randao.BLSRevealLen)}},
	}
	foldBlockDeclaredReveals(block) // must not panic

	if len(defaultEntropyAccumulatorStore.accs) != 0 {
		t.Fatal("a failed Accumulator construction must not be cached")
	}
}

// --- entropyAccumulatorFor ---------------------------------------------------

func TestEntropyAccumulatorFor_ConstructsAndCaches(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	kr := withRevealKeyring(t)
	kr.add("peer-a")

	a1, err := entropyAccumulatorFor(3)
	if err != nil {
		t.Fatalf("entropyAccumulatorFor: %v", err)
	}
	a2, err := entropyAccumulatorFor(3)
	if err != nil {
		t.Fatalf("entropyAccumulatorFor (second call): %v", err)
	}
	if a1 != a2 {
		t.Fatal("a different Accumulator came back on the second call for the same epoch — it must be cached; reconstructing would silently discard everything folded so far")
	}
}

func TestEntropyAccumulatorFor_ExpectedSetMatchesCommittee(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	kr := withRevealKeyring(t)
	kr.add("peer-a")
	kr.add("peer-b")
	kr.add("peer-c")

	acc, err := entropyAccumulatorFor(3)
	if err != nil {
		t.Fatalf("entropyAccumulatorFor: %v", err)
	}
	if acc.Expected() != 3 {
		t.Fatalf("Accumulator expects %d, want 3 (the committee's members)", acc.Expected())
	}
}

// An epoch with no anchored pool has no accumulator, and says so by name.
func TestEntropyAccumulatorFor_UnanchoredEpochIsNamed(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	withRevealKeyring(t) // no members: the source reports "not anchored"

	if _, err := entropyAccumulatorFor(3); !errors.Is(err, ErrBLSRevealNeedsAnchoredPool) {
		t.Fatalf("err = %v, want ErrBLSRevealNeedsAnchoredPool", err)
	}
	if len(defaultEntropyAccumulatorStore.accs) != 0 {
		t.Fatal("a failed Accumulator construction must not be cached")
	}
}

// --- foldBlockDeclaredReveals: the live-fold path ---------------------------

func TestFoldBlockDeclaredReveals_ValidBLSReveal_Folds(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	kr := withRevealKeyring(t)
	kr.add("peer-a")

	acc, err := entropyAccumulatorFor(3)
	if err != nil {
		t.Fatalf("entropyAccumulatorFor: %v", err)
	}
	block := &config.ZKBlock{
		BlockNumber:   100,
		Slot:          3 * N, // EpochForSlot(3*N) == 3
		RandaoReveals: []config.Reveal{{ProposerID: "peer-a", Secret: kr.reveal("peer-a", 3)}},
	}
	foldBlockDeclaredReveals(block)

	if acc.Count() != 1 {
		t.Fatalf("Accumulator.Count() = %d, want 1 — a correctly signed reveal must fold with no commit phase anywhere", acc.Count())
	}
}

func TestFoldBlockDeclaredReveals_RejectsBadReveals(t *testing.T) {
	kr := withRevealKeyring(t)
	kr.add("peer-a")
	kr.add("peer-out")
	kr.setMembers(3, "peer-a") // peer-out has a key but no seat in epoch 3

	good := kr.reveal("peer-a", 3)
	corrupted := append([]byte{}, good...)
	corrupted[0] ^= 0xFF

	cases := []struct {
		name   string
		reveal config.Reveal
	}{
		{"signature for a different epoch", config.Reveal{ProposerID: "peer-a", Secret: kr.reveal("peer-a", 4)}},
		{"another peer's signature under this peer's ID", config.Reveal{ProposerID: "peer-a", Secret: kr.reveal("peer-out", 3)}},
		{"corrupted signature", config.Reveal{ProposerID: "peer-a", Secret: corrupted}},
		{"empty reveal", config.Reveal{ProposerID: "peer-a", Secret: nil}},
		{"identity point", config.Reveal{ProposerID: "peer-a", Secret: make([]byte, randao.BLSRevealLen)}},
		{"proposer not on the committee", config.Reveal{ProposerID: "peer-out", Secret: kr.reveal("peer-out", 3)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetEntropyAccumulatorStore(t)
			acc, err := entropyAccumulatorFor(3)
			if err != nil {
				t.Fatalf("entropyAccumulatorFor: %v", err)
			}
			block := &config.ZKBlock{
				BlockNumber:   100,
				Slot:          3 * N,
				RandaoReveals: []config.Reveal{tc.reveal},
			}
			foldBlockDeclaredReveals(block) // must not panic

			if acc.Count() != 0 {
				t.Fatalf("Accumulator.Count() = %d, want 0 — this reveal must not have folded", acc.Count())
			}
		})
	}
}

func TestFoldBlockDeclaredReveals_EpochComesFromBlockSlot_NotLiveStores(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	kr := withRevealKeyring(t)
	kr.add("peer-a")
	// Deliberately leave DefaultSlotStore/DefaultProposerPeriods wherever other
	// tests left them — the epoch must come only from block.Slot.
	block := &config.ZKBlock{
		BlockNumber:   200,
		Slot:          7 * N, // epoch 7
		RandaoReveals: []config.Reveal{{ProposerID: "peer-a", Secret: kr.reveal("peer-a", 7)}},
	}
	foldBlockDeclaredReveals(block)

	acc, ok := defaultEntropyAccumulatorStore.accs[7]
	if !ok {
		t.Fatalf("expected an Accumulator for epoch 7 (block.Slot=%d); present: %v",
			block.Slot, keysOfAccs(defaultEntropyAccumulatorStore.accs))
	}
	if acc.Count() != 1 {
		t.Fatalf("epoch 7 reveal did not fold: Count() = %d", acc.Count())
	}
}

func keysOfAccs(m map[uint64]*randao.Accumulator) []uint64 {
	out := make([]uint64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- the production side: entropy_reveal_produce.go -------------------------

func TestSetNodeIdentity_RejectsBadInput(t *testing.T) {
	priv, _ := newTestIdentity(t)
	_, otherID := newTestIdentity(t)

	if err := SetNodeIdentity(nil, "x"); err == nil {
		t.Fatal("nil key must be rejected")
	}
	if err := SetNodeIdentity(priv, ""); err == nil {
		t.Fatal("empty peer ID must be rejected")
	}
	if err := SetNodeIdentity(priv, otherID); !errors.Is(err, ErrNodeIdentityMismatch) {
		t.Fatalf("a key that does not match the claimed peer ID must be rejected at install time, got %v", err)
	}
}

func TestProduceRevealForEpoch_FailsClosedWithoutIdentity(t *testing.T) {
	nodeIdentityMu.Lock()
	savedPeer := nodeIdentityPeer
	nodeIdentityPeer = ""
	nodeIdentityMu.Unlock()
	t.Cleanup(func() {
		nodeIdentityMu.Lock()
		nodeIdentityPeer = savedPeer
		nodeIdentityMu.Unlock()
	})

	if _, err := ProduceRevealForEpoch(3); !errors.Is(err, ErrNoNodeIdentity) {
		t.Fatalf("producing a reveal with no installed identity must fail closed, got %v", err)
	}
}

func TestProduceRevealForEpoch_NotSeated_ReturnsError(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	priv, id := newTestIdentity(t)
	kr := withRevealKeyring(t)
	kr.add(id)
	kr.add("peer-seated")
	kr.setMembers(3, "peer-seated") // a committee of one, and it isn't us
	withNodeIdentity(t, priv, id)
	kr.useKeyOf(t, id)

	if _, err := ProduceRevealForEpoch(3); !errors.Is(err, ErrNotOnEntropyCommittee) {
		t.Fatalf("a node not on the entropy committee must not produce a reveal, got %v", err)
	}
}

// The round trip that matters: this node produces its own reveal, a block
// declares it, and the fold accepts it — with no commit phase and nothing
// persisted between the two.
func TestProduceRevealForEpoch_RoundTripsThroughFold(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	priv, id := newTestIdentity(t)
	kr := withRevealKeyring(t)
	kr.add(id)
	withNodeIdentity(t, priv, id)
	kr.useKeyOf(t, id)

	seated, err := SelfOnEntropyCommittee(5)
	if err != nil {
		t.Fatalf("SelfOnEntropyCommittee: %v", err)
	}
	if !seated {
		t.Fatal("the only member must be seated on the entropy committee")
	}

	sig, err := ProduceRevealForEpoch(5)
	if err != nil {
		t.Fatalf("ProduceRevealForEpoch: %v", err)
	}
	if len(sig) != randao.BLSRevealLen {
		t.Fatalf("reveal is %d bytes, want %d", len(sig), randao.BLSRevealLen)
	}
	if string(sig) != string(kr.reveal(id, 5)) {
		t.Fatal("produced reveal is not the member's BLS reveal")
	}

	// Determinism: re-producing after a simulated restart must give the same
	// bytes. This is the property that removed the durable secret store.
	again, err := ProduceRevealForEpoch(5)
	if err != nil {
		t.Fatalf("ProduceRevealForEpoch (again): %v", err)
	}
	if string(sig) != string(again) {
		t.Fatal("re-producing the reveal gave different bytes — a restart mid-epoch would create a second valid reveal")
	}

	acc, err := entropyAccumulatorFor(5)
	if err != nil {
		t.Fatalf("entropyAccumulatorFor: %v", err)
	}
	foldBlockDeclaredReveals(&config.ZKBlock{
		BlockNumber:   300,
		Slot:          5 * N,
		RandaoReveals: []config.Reveal{{ProposerID: id, Secret: sig}},
	})

	if acc.Count() != 1 {
		t.Fatalf("Accumulator.Count() = %d, want 1 — a self-produced reveal must fold", acc.Count())
	}
	if !acc.Complete() {
		t.Fatal("the single expected member revealed, so the epoch must be complete (mix, not fallback)")
	}
}

// A seated node whose BLS key is not the one its snapshot binds must not stay
// silent: the push path reports the mismatch instead of "nothing to push".
func TestPushOwnRevealForSlot_KeyMismatchIsReported(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	resetRevealInbox(t)
	priv, id := newTestIdentity(t)
	_, proposerID := newTestIdentity(t)
	proposer, err := peer.Decode(proposerID)
	if err != nil {
		t.Fatal(err)
	}
	kr := withRevealKeyring(t)
	kr.add(id)
	kr.add("peer-other")
	withNodeIdentity(t, priv, id)
	kr.useKeyOf(t, "peer-other") // this node holds the wrong key

	if err := PushOwnRevealForSlot(9*N, proposer); !errors.Is(err, ErrRevealBLSKeyMismatch) {
		t.Fatalf("err = %v, want ErrRevealBLSKeyMismatch", err)
	}
}
