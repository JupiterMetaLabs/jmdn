package messaging

// Tests for the reveal inbox and the block-assembly path — the link that was
// missing between "a reveal can be produced" and "a reveal reaches the fold".

import (
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
)

func resetRevealInbox(t *testing.T) {
	t.Helper()
	saved := defaultRevealInbox
	defaultRevealInbox = &revealInbox{byEpoch: make(map[uint64]map[string][]byte)}
	t.Cleanup(func() { defaultRevealInbox = saved })
}

func TestAddInboundReveal_AcceptsValidRejectsInvalid(t *testing.T) {
	resetRevealInbox(t)
	priv, id := newTestIdentity(t)
	_, otherID := newTestIdentity(t)
	chainID := BLS_Signer.DomainChainID()

	good, err := randao.SignReveal(priv, chainID, 4, id)
	if err != nil {
		t.Fatalf("SignReveal: %v", err)
	}

	if err := AddInboundReveal(4, id, good); err != nil {
		t.Fatalf("a valid reveal was rejected: %v", err)
	}
	if got := InboxCountForEpoch(4); got != 1 {
		t.Fatalf("inbox has %d, want 1", got)
	}

	// Wrong epoch, wrong claimed peer, junk bytes, empty peer — all must fail.
	if err := AddInboundReveal(5, id, good); err == nil {
		t.Fatal("a reveal for epoch 4 must not be accepted as epoch 5")
	}
	if err := AddInboundReveal(4, otherID, good); err == nil {
		t.Fatal("one peer's reveal must not be accepted under another peer's ID")
	}
	if err := AddInboundReveal(4, id, make([]byte, randao.Ed25519SigLen)); err == nil {
		t.Fatal("a zero signature must not be accepted")
	}
	if err := AddInboundReveal(4, "", good); err == nil {
		t.Fatal("an empty peer ID must be rejected")
	}
}

// Identical re-delivery must be a no-op: §4.4 pushes once per slot across the
// window, so the same reveal arrives repeatedly by design.
func TestAddInboundReveal_IdenticalRedeliveryIsNoOp(t *testing.T) {
	resetRevealInbox(t)
	priv, id := newTestIdentity(t)
	sig, err := randao.SignReveal(priv, BLS_Signer.DomainChainID(), 4, id)
	if err != nil {
		t.Fatalf("SignReveal: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := AddInboundReveal(4, id, sig); err != nil {
			t.Fatalf("re-delivery %d rejected: %v", i, err)
		}
	}
	if got := InboxCountForEpoch(4); got != 1 {
		t.Fatalf("inbox has %d entries after 5 identical deliveries, want 1", got)
	}
}

// RevealsForBlock must be empty outside the reveal window — a reveal declared
// after the cutoff is one Finalise() has already stopped waiting for.
func TestRevealsForBlock_OnlyInsideTheRevealWindow(t *testing.T) {
	resetRevealInbox(t)
	priv, id := newTestIdentity(t)
	sig, err := randao.SignReveal(priv, BLS_Signer.DomainChainID(), 3, id)
	if err != nil {
		t.Fatalf("SignReveal: %v", err)
	}
	if err := AddInboundReveal(3, id, sig); err != nil {
		t.Fatalf("AddInboundReveal: %v", err)
	}

	// Inside [3*N, 3*N+K)
	for slot := uint64(3) * N; slot < cutoffSlotFor(3); slot++ {
		got := RevealsForBlock(slot)
		if len(got) != 1 {
			t.Fatalf("slot %d (inside the window) returned %d reveals, want 1", slot, len(got))
		}
		if got[0].ProposerID != id {
			t.Fatalf("slot %d returned proposer %q, want %q", slot, got[0].ProposerID, id)
		}
		if len(got[0].Secret) != randao.Ed25519SigLen {
			t.Fatalf("reveal is %d bytes, want %d", len(got[0].Secret), randao.Ed25519SigLen)
		}
	}

	// At and after the cutoff, and in the epoch's tail.
	for _, slot := range []uint64{cutoffSlotFor(3), cutoffSlotFor(3) + 1, 4*N - 1} {
		if got := RevealsForBlock(slot); len(got) != 0 {
			t.Fatalf("slot %d is past the cutoff but returned %d reveals", slot, len(got))
		}
	}
}

// Deterministic order matters: the reveal list is hash-covered in array order
// under M2b, so two nodes assembling from the same inbox must agree byte for
// byte.
func TestRevealsForBlock_SortedByPeerID(t *testing.T) {
	resetRevealInbox(t)
	chainID := BLS_Signer.DomainChainID()

	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		priv, id := newTestIdentity(t)
		sig, err := randao.SignReveal(priv, chainID, 2, id)
		if err != nil {
			t.Fatalf("SignReveal: %v", err)
		}
		if err := AddInboundReveal(2, id, sig); err != nil {
			t.Fatalf("AddInboundReveal: %v", err)
		}
		ids = append(ids, id)
	}

	got := RevealsForBlock(uint64(2) * N)
	if len(got) != len(ids) {
		t.Fatalf("got %d reveals, want %d", len(got), len(ids))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ProposerID >= got[i].ProposerID {
			t.Fatalf("reveals are not sorted by peer ID: %q then %q", got[i-1].ProposerID, got[i].ProposerID)
		}
	}

	// And repeated calls must be identical.
	again := RevealsForBlock(uint64(2) * N)
	for i := range got {
		if got[i].ProposerID != again[i].ProposerID || string(got[i].Secret) != string(again[i].Secret) {
			t.Fatal("two calls produced different lists — block assembly would be non-deterministic")
		}
	}
}

func TestPruneRevealsBelow_DropsClosedEpochs(t *testing.T) {
	resetRevealInbox(t)
	chainID := BLS_Signer.DomainChainID()
	for epoch := uint64(1); epoch <= 5; epoch++ {
		priv, id := newTestIdentity(t)
		sig, err := randao.SignReveal(priv, chainID, epoch, id)
		if err != nil {
			t.Fatalf("SignReveal: %v", err)
		}
		if err := AddInboundReveal(epoch, id, sig); err != nil {
			t.Fatalf("AddInboundReveal: %v", err)
		}
	}

	pruneRevealsBelow(4)

	for epoch := uint64(1); epoch < 4; epoch++ {
		if got := InboxCountForEpoch(epoch); got != 0 {
			t.Fatalf("epoch %d survived pruning (has %d)", epoch, got)
		}
	}
	for epoch := uint64(4); epoch <= 5; epoch++ {
		if got := InboxCountForEpoch(epoch); got != 1 {
			t.Fatalf("epoch %d was pruned but is at or above the watermark (has %d)", epoch, got)
		}
	}
}

// End to end through the real production path: install an identity, be the sole
// eligible peer, and confirm the reveal makes it all the way from
// ProduceRevealForEpoch into what a block would declare, and then folds.
func TestEndToEnd_ProduceToBlockToFold(t *testing.T) {
	resetRevealInbox(t)
	resetEntropyAccumulatorStore(t)
	priv, id := newTestIdentity(t)

	withNodeIdentity(t, priv, id)
	withBeaconEntropy(t, map[uint64][]byte{6: fakeEntropy(0x5A, 32)})
	wireEligibilityWithPeers(t, []string{id})

	// Block assembly at a slot inside epoch 6's reveal window.
	slot := uint64(6) * N
	declared := RevealsForBlock(slot)
	if len(declared) != 1 {
		t.Fatalf("block assembly produced %d reveals, want 1 — this is the link that used to be missing", len(declared))
	}
	if declared[0].ProposerID != id {
		t.Fatalf("declared reveal is from %q, want this node %q", declared[0].ProposerID, id)
	}

	// Now fold it as a committed block would.
	acc, err := entropyAccumulatorFor(6)
	if err != nil {
		t.Fatalf("entropyAccumulatorFor: %v", err)
	}
	foldBlockDeclaredReveals(&config.ZKBlock{
		BlockNumber:   100,
		Slot:          slot,
		RandaoReveals: declared,
	})
	if acc.Count() != 1 {
		t.Fatalf("Accumulator.Count() = %d, want 1", acc.Count())
	}
	if !acc.Complete() {
		t.Fatal("the sole expected member revealed, so the epoch must be complete — mix, not fallback")
	}
	if res := acc.Finalise(); res.Outcome != randao.OutcomeMixed {
		t.Fatalf("Outcome = %v, want OutcomeMixed", res.Outcome)
	}
}
