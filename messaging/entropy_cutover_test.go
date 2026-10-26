package messaging

// The entropy cut-over (entropy_cutover.go): Ed25519 reveals and the legacy
// VDF before it, BLS reveals and the fixed VDF from it, decided by epoch alone.

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
)

// The testnet's cut-over is pinned; any other chain uses the fixed rules from
// epoch 0.
func TestEntropyCutoverByChain(t *testing.T) {
	if got := entropyCutoverByChain[8000800]; got != 60 {
		t.Fatalf("testnet cut-over = %d, want 60 (slot 3000)", got)
	}
	if _, listed := entropyCutoverByChain[7000700]; listed {
		t.Fatal("mainnet has no Stage-2 history and must not be listed (fixed rules from epoch 0)")
	}
	withEntropyCutover(t, 52)
	if afterEntropyCutover(51) || !afterEntropyCutover(52) || !afterEntropyCutover(1<<40) {
		t.Fatal("BLS reveals apply from the cut-over epoch on, never before")
	}
	if VDFCutoverEpoch() != 53 {
		t.Fatalf("the first fixed-rule entropy must be the one sealed from the cut-over epoch's mix (53), got %d", VDFCutoverEpoch())
	}
	withEntropyCutover(t, 0)
	if !afterEntropyCutover(0) || VDFCutoverEpoch() != 1 {
		t.Fatal("cut-over 0 must mean the fixed rules for every epoch")
	}
}

// One member, both sides of the cut-over: the epoch before it takes only the
// Ed25519 reveal, the cut-over epoch takes only the BLS reveal - in the inbox
// and in the fold.
func TestCutover_RevealSchemeByEpoch(t *testing.T) {
	const cut = 10
	edPriv, id := newTestIdentity(t)
	kr := withRevealKeyring(t) // BLS committee source for epochs >= cut
	kr.add(id)
	withEntropyCutover(t, cut)
	withBeaconEntropy(t, map[uint64][]byte{cut - 1: fakeEntropy(0x99, 32)})
	wireEligibilityWithPeers(t, []string{id}) // pre-cut-over committee source
	chainID := BLS_Signer.DomainChainID()

	edOld, err := randao.SignReveal(edPriv, chainID, cut-1, id)
	if err != nil {
		t.Fatal(err)
	}
	blsOld, err := randao.SignRevealBLS(kr.keys[id].priv, chainID, cut-1, id)
	if err != nil {
		t.Fatal(err)
	}
	edNew, err := randao.SignReveal(edPriv, chainID, cut, id)
	if err != nil {
		t.Fatal(err)
	}
	blsNew := kr.reveal(id, cut)

	// Inbox.
	resetRevealInbox(t)
	if err := AddInboundReveal(cut-1, id, blsOld); !errors.Is(err, ErrRevealDidNotVerify) {
		t.Fatalf("before the cut-over a BLS reveal must be refused, got %v", err)
	}
	if err := AddInboundReveal(cut-1, id, edOld); err != nil {
		t.Fatalf("before the cut-over the Ed25519 reveal must be accepted: %v", err)
	}
	if err := AddInboundReveal(cut, id, edNew); !errors.Is(err, ErrRevealDidNotVerify) {
		t.Fatalf("from the cut-over an Ed25519 reveal must be refused, got %v", err)
	}
	if err := AddInboundReveal(cut, id, blsNew); err != nil {
		t.Fatalf("from the cut-over the BLS reveal must be accepted: %v", err)
	}

	// Fold, as a replaying or syncing node does with committed blocks.
	for _, tc := range []struct {
		name   string
		epoch  uint64
		reveal []byte
		folds  bool
	}{
		{"old epoch, Ed25519", cut - 1, edOld, true},
		{"old epoch, BLS", cut - 1, blsOld, false},
		{"cut-over epoch, Ed25519", cut, edNew, false},
		{"cut-over epoch, BLS", cut, blsNew, true},
	} {
		resetEntropyAccumulatorStore(t)
		acc, err := entropyAccumulatorFor(tc.epoch)
		if err != nil {
			t.Fatalf("%s: entropyAccumulatorFor: %v", tc.name, err)
		}
		foldBlockDeclaredReveals(&config.ZKBlock{
			BlockNumber:   tc.epoch * N,
			Slot:          tc.epoch * N,
			RandaoReveals: []config.Reveal{{ProposerID: id, Secret: tc.reveal}},
		})
		if got := acc.Count() == 1; got != tc.folds {
			t.Fatalf("%s: folded=%v, want %v", tc.name, got, tc.folds)
		}
	}
}

// A node produces the reveal its epoch calls for: Ed25519 with its libp2p key
// before the cut-over, BLS with its committee key from it.
func TestCutover_ProduceRevealByEpoch(t *testing.T) {
	const cut = 10
	priv, id := newTestIdentity(t)
	kr := withRevealKeyring(t)
	kr.add(id)
	kr.useKeyOf(t, id)
	withEntropyCutover(t, cut)
	withBeaconEntropy(t, map[uint64][]byte{cut - 1: fakeEntropy(0x98, 32)})
	wireEligibilityWithPeers(t, []string{id})
	withNodeIdentity(t, priv, id)

	old, err := ProduceRevealForEpoch(cut - 1)
	if err != nil {
		t.Fatalf("pre-cut-over reveal: %v", err)
	}
	if !randao.NewEd25519RevealVerifier().VerifyReveal(id, BLS_Signer.DomainChainID(), cut-1, old) {
		t.Fatal("before the cut-over the node must produce its Ed25519 reveal")
	}
	now, err := ProduceRevealForEpoch(cut)
	if err != nil {
		t.Fatalf("cut-over reveal: %v", err)
	}
	if string(now) != string(kr.reveal(id, cut)) {
		t.Fatal("from the cut-over the node must produce its BLS reveal")
	}
}

// The boundary block's VDF identity is unchanged before the VDF cut-over epoch
// (so the old build and the new one agree there) and changes from it (so a
// node that missed the upgrade is refused by name, not silently split).
func TestCutover_VDFIdentityByEpoch(t *testing.T) {
	prev := LocalVDFIdentity()
	SetLocalVDFIdentity("aaaa")
	t.Cleanup(func() { SetLocalVDFIdentity(prev) })
	withEntropyCutover(t, 52)

	if VDFIdentityForEpoch(52) != "aaaa" {
		t.Fatal("entropy sealed before the VDF cut-over keeps the old identity")
	}
	fixed := VDFIdentityForEpoch(53)
	if fixed == "aaaa" || fixed == "" || VDFIdentityForEpoch(60) != fixed {
		t.Fatal("from the VDF cut-over the identity must be the new one, the same for every epoch")
	}
	SetLocalVDFIdentity("")
	if VDFIdentityForEpoch(60) != "" {
		t.Fatal("Stage 1 (no identity) stays empty")
	}
}
