package messaging

// Tests for entropy_reveal.go and entropy_reveal_produce.go — Stage B/C after
// the 2026-08-20 rewrite to Architecture §4.3 Decision A (ed25519 reveals).
//
// The single biggest difference from the previous version of this file: the
// old tests had to simulate a commit phase that no production code implements
// (calling Round.AddCommit and Round.CloseCommit by hand) just to get a reveal
// to verify at all. Under Decision A there is no commit phase to simulate — a
// reveal is produced and verified with the node's own identity key and nothing
// else — so these tests exercise the real production path end to end.

import (
	"fmt"
	"testing"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/JupiterMetaLabs/avc/randao"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
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

// newTestIdentity returns a real ed25519 libp2p identity — the same kind
// node/node.go creates. Decision A verifies against the peer ID itself, so
// tests need genuine self-certifying IDs; the "peer-00" placeholders
// wireEligibility uses cannot carry a key.
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

// wireEligibilityWithPeers is wireEligibility with caller-supplied peer IDs, so
// the eligible pool can be made of real ed25519 identities.
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

	block := &config.ZKBlock{
		BlockNumber:   1,
		Slot:          10,
		RandaoReveals: []config.Reveal{{ProposerID: "v1", Secret: make([]byte, randao.Ed25519SigLen)}},
	}
	foldBlockDeclaredReveals(block) // must not panic

	if len(defaultEntropyAccumulatorStore.accs) != 0 {
		t.Fatal("a failed Accumulator construction must not be cached")
	}
}

// --- entropyAccumulatorFor ---------------------------------------------------

func TestEntropyAccumulatorFor_ConstructsAndCaches(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	_, id := newTestIdentity(t)
	withBeaconEntropy(t, map[uint64][]byte{3: fakeEntropy(0xAA, 32)})
	wireEligibilityWithPeers(t, []string{id})

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

func TestEntropyAccumulatorFor_ExpectedSetMatchesSelectedCommittee(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	_, id := newTestIdentity(t)
	withBeaconEntropy(t, map[uint64][]byte{3: fakeEntropy(0xBB, 32)})
	wireEligibilityWithPeers(t, []string{id}) // pool < m=13 -> committee is the whole pool

	acc, err := entropyAccumulatorFor(3)
	if err != nil {
		t.Fatalf("entropyAccumulatorFor: %v", err)
	}
	if acc.Expected() != 1 {
		t.Fatalf("Accumulator expects %d, want 1 (the single wired eligible peer)", acc.Expected())
	}
}

// --- foldBlockDeclaredReveals: the live-fold path ---------------------------

func TestFoldBlockDeclaredReveals_ValidEd25519Reveal_Folds(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	priv, id := newTestIdentity(t)
	withBeaconEntropy(t, map[uint64][]byte{3: fakeEntropy(0xCC, 32)})
	wireEligibilityWithPeers(t, []string{id})

	acc, err := entropyAccumulatorFor(3)
	if err != nil {
		t.Fatalf("entropyAccumulatorFor: %v", err)
	}

	sig, err := randao.SignReveal(priv, BLS_Signer.DomainChainID(), 3, id)
	if err != nil {
		t.Fatalf("SignReveal: %v", err)
	}

	block := &config.ZKBlock{
		BlockNumber:   100,
		Slot:          3 * N, // EpochForSlot(3*N) == 3
		RandaoReveals: []config.Reveal{{ProposerID: id, Secret: sig}},
	}
	foldBlockDeclaredReveals(block)

	if acc.Count() != 1 {
		t.Fatalf("Accumulator.Count() = %d, want 1 — a correctly signed reveal must fold with no commit phase anywhere", acc.Count())
	}
}

func TestFoldBlockDeclaredReveals_RejectsBadReveals(t *testing.T) {
	priv, id := newTestIdentity(t)
	otherPriv, otherID := newTestIdentity(t)
	chainID := BLS_Signer.DomainChainID()

	goodForEpoch3, err := randao.SignReveal(priv, chainID, 3, id)
	if err != nil {
		t.Fatalf("SignReveal: %v", err)
	}
	wrongEpoch, err := randao.SignReveal(priv, chainID, 4, id)
	if err != nil {
		t.Fatalf("SignReveal wrong epoch: %v", err)
	}
	otherPeersSig, err := randao.SignReveal(otherPriv, chainID, 3, otherID)
	if err != nil {
		t.Fatalf("SignReveal other peer: %v", err)
	}
	corrupted := append([]byte{}, goodForEpoch3...)
	corrupted[0] ^= 0xFF

	cases := []struct {
		name   string
		reveal config.Reveal
	}{
		{"signature for a different epoch", config.Reveal{ProposerID: id, Secret: wrongEpoch}},
		{"another peer's signature under this peer's ID", config.Reveal{ProposerID: id, Secret: otherPeersSig}},
		{"corrupted signature", config.Reveal{ProposerID: id, Secret: corrupted}},
		{"empty reveal", config.Reveal{ProposerID: id, Secret: nil}},
		{"proposer not on the committee", config.Reveal{ProposerID: otherID, Secret: otherPeersSig}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetEntropyAccumulatorStore(t)
			withBeaconEntropy(t, map[uint64][]byte{3: fakeEntropy(0xDD, 32)})
			wireEligibilityWithPeers(t, []string{id})

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
	priv, id := newTestIdentity(t)
	// Deliberately leave DefaultSlotStore/DefaultPeriodStore wherever other
	// tests left them — the epoch must come only from block.Slot.
	withBeaconEntropy(t, map[uint64][]byte{7: fakeEntropy(0xEE, 32)})
	wireEligibilityWithPeers(t, []string{id})

	sig, err := randao.SignReveal(priv, BLS_Signer.DomainChainID(), 7, id)
	if err != nil {
		t.Fatalf("SignReveal: %v", err)
	}
	block := &config.ZKBlock{
		BlockNumber:   200,
		Slot:          7 * N, // epoch 7
		RandaoReveals: []config.Reveal{{ProposerID: id, Secret: sig}},
	}
	foldBlockDeclaredReveals(block)

	if _, ok := defaultEntropyAccumulatorStore.accs[7]; !ok {
		t.Fatalf("expected an Accumulator for epoch 7 (block.Slot=%d); present: %v",
			block.Slot, keysOfAccs(defaultEntropyAccumulatorStore.accs))
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
	if err := SetNodeIdentity(priv, otherID); err == nil {
		t.Fatal("a key that does not match the claimed peer ID must be rejected at install time, " +
			"not silently accepted and then rejected by every verifier every epoch")
	}
}

func TestProduceRevealForEpoch_FailsClosedWithoutIdentity(t *testing.T) {
	nodeIdentityMu.Lock()
	savedPriv, savedPeer := nodeIdentityPriv, nodeIdentityPeer
	nodeIdentityPriv, nodeIdentityPeer = nil, ""
	nodeIdentityMu.Unlock()
	t.Cleanup(func() {
		nodeIdentityMu.Lock()
		nodeIdentityPriv, nodeIdentityPeer = savedPriv, savedPeer
		nodeIdentityMu.Unlock()
	})

	if _, err := ProduceRevealForEpoch(3); err == nil {
		t.Fatal("producing a reveal with no installed identity must fail closed")
	}
}

func TestProduceRevealForEpoch_NotSeated_ReturnsError(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	priv, id := newTestIdentity(t)
	_, seatedID := newTestIdentity(t)

	withNodeIdentity(t, priv, id)
	withBeaconEntropy(t, map[uint64][]byte{3: fakeEntropy(0xAB, 32)})
	wireEligibilityWithPeers(t, []string{seatedID}) // pool of one, and it isn't us

	if _, err := ProduceRevealForEpoch(3); err == nil {
		t.Fatal("a node not on the entropy committee must not produce a reveal")
	}
}

// The round trip that matters: this node produces its own reveal, a block
// declares it, and the fold accepts it — with no commit phase and nothing
// persisted between the two.
func TestProduceRevealForEpoch_RoundTripsThroughFold(t *testing.T) {
	resetEntropyAccumulatorStore(t)
	priv, id := newTestIdentity(t)

	withNodeIdentity(t, priv, id)
	withBeaconEntropy(t, map[uint64][]byte{5: fakeEntropy(0xCD, 32)})
	wireEligibilityWithPeers(t, []string{id})

	seated, err := SelfOnEntropyCommittee(5)
	if err != nil {
		t.Fatalf("SelfOnEntropyCommittee: %v", err)
	}
	if !seated {
		t.Fatal("the only eligible peer must be seated on the entropy committee")
	}

	sig, err := ProduceRevealForEpoch(5)
	if err != nil {
		t.Fatalf("ProduceRevealForEpoch: %v", err)
	}
	if len(sig) != randao.Ed25519SigLen {
		t.Fatalf("reveal is %d bytes, want %d", len(sig), randao.Ed25519SigLen)
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
