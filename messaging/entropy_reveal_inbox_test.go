package messaging

// Tests for the reveal inbox and the block-assembly path — the link that was
// missing between "a reveal can be produced" and "a reveal reaches the fold".

import (
	"fmt"
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"

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
	kr := withRevealKeyring(t)
	kr.add("peer-a")
	kr.add("peer-b")
	good := kr.reveal("peer-a", 4)

	if err := AddInboundReveal(4, "peer-a", good); err != nil {
		t.Fatalf("a valid reveal was rejected: %v", err)
	}
	if got := InboxCountForEpoch(4); got != 1 {
		t.Fatalf("inbox has %d, want 1", got)
	}

	// Wrong epoch, wrong claimed peer, identity point, empty peer — all must fail.
	if err := AddInboundReveal(5, "peer-a", good); err == nil {
		t.Fatal("a reveal for epoch 4 must not be accepted as epoch 5")
	}
	if err := AddInboundReveal(4, "peer-b", good); err == nil {
		t.Fatal("one peer's reveal must not be accepted under another peer's ID")
	}
	if err := AddInboundReveal(4, "peer-a", make([]byte, randao.BLSRevealLen)); err == nil {
		t.Fatal("a zero (identity-point) signature must not be accepted")
	}
	if err := AddInboundReveal(4, "", good); err == nil {
		t.Fatal("an empty peer ID must be rejected")
	}
}

// Identical re-delivery must be a no-op: §4.4 pushes once per slot across the
// window, so the same reveal arrives repeatedly by design.
func TestAddInboundReveal_IdenticalRedeliveryIsNoOp(t *testing.T) {
	resetRevealInbox(t)
	kr := withRevealKeyring(t)
	kr.add("peer-a")
	sig := kr.reveal("peer-a", 4)
	for i := 0; i < 5; i++ {
		if err := AddInboundReveal(4, "peer-a", sig); err != nil {
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
	kr := withRevealKeyring(t)
	kr.add("peer-a")
	if err := AddInboundReveal(3, "peer-a", kr.reveal("peer-a", 3)); err != nil {
		t.Fatalf("AddInboundReveal: %v", err)
	}

	// Inside [3*N, 3*N+K)
	for slot := uint64(3) * N; slot < cutoffSlotFor(3); slot++ {
		got := RevealsForBlock(slot)
		if len(got) != 1 {
			t.Fatalf("slot %d (inside the window) returned %d reveals, want 1", slot, len(got))
		}
		if got[0].ProposerID != "peer-a" {
			t.Fatalf("slot %d returned proposer %q, want %q", slot, got[0].ProposerID, "peer-a")
		}
		if len(got[0].Secret) != randao.BLSRevealLen {
			t.Fatalf("reveal is %d bytes, want %d", len(got[0].Secret), randao.BLSRevealLen)
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
	kr := withRevealKeyring(t)

	ids := []string{"peer-d", "peer-b", "peer-e", "peer-a", "peer-c"} // added out of order
	for _, id := range ids {
		kr.add(id)
	}
	for _, id := range ids {
		if err := AddInboundReveal(2, id, kr.reveal(id, 2)); err != nil {
			t.Fatalf("AddInboundReveal: %v", err)
		}
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
	kr := withRevealKeyring(t)
	for epoch := uint64(1); epoch <= 5; epoch++ {
		id := fmt.Sprintf("peer-%d", epoch)
		kr.add(id)
		if err := AddInboundReveal(epoch, id, kr.reveal(id, epoch)); err != nil {
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

// End to end through the real production path: install an identity and a BLS
// key, be the sole member, and confirm the reveal makes it all the way from
// ProduceRevealForEpoch into what a block would declare, and then folds.
func TestEndToEnd_ProduceToBlockToFold(t *testing.T) {
	resetRevealInbox(t)
	resetEntropyAccumulatorStore(t)
	priv, id := newTestIdentity(t)
	kr := withRevealKeyring(t)
	kr.add(id)
	withNodeIdentity(t, priv, id)
	kr.useKeyOf(t, id)

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
