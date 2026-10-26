package messaging

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	seedcommittee "gossipnode/seednode/committee"
)

// blsEnv is an anchored entropy committee for epoch whose members hold real
// BLS keys. keys[i] belongs to peers[i].
type blsEnv struct {
	epoch uint64
	peers []string
	keys  []testAuthority
}

func withBLSRevealEpoch(t *testing.T, peers ...string) *blsEnv {
	t.Helper()
	const epoch = 21 // cutoff 21*N-K = 1047 >= the anchor's slot 1000
	env := withAnchoring(t, 860)

	withEntropyCutover(t, 0) // every epoch is a BLS epoch in these tests
	be := &blsEnv{epoch: epoch, peers: peers}
	entries := make([]seedcommittee.CommitteeEntry, 0, len(peers))
	for i, p := range peers {
		k := newTestAuthority(t)
		be.keys = append(be.keys, k)
		entries = append(entries, seedcommittee.CommitteeEntry{
			PeerID: p, BLSPub: k.pubHex, RewardAddress: fmt.Sprintf("0x%040x", i+1),
		})
	}
	sig, err := blssign.BLSSign(env.auth.priv, seedcommittee.CanonicalCommitteeBytes(500, "", entries))
	if err != nil {
		t.Fatal(err)
	}
	snap := &seedcommittee.CommitteeSnapshot{Epoch: 500, Entries: entries, AuthorityPubHex: env.auth.pubHex, Signature: hex.EncodeToString(sig)}
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 1000, snap)); err != nil {
		t.Fatal(err)
	}
	env.setTip(879) // the 860 anchor is the newest committed one
	withBeaconEntropy(t, map[uint64][]byte{epoch: fakeEntropy(0x5a, 32)})

	resetRevealInbox(t)
	pruneBLSRevealCommitteesBelow(^uint64(0))
	t.Cleanup(func() { pruneBLSRevealCommitteesBelow(^uint64(0)) })
	return be
}

func (be *blsEnv) reveal(t *testing.T, i int) []byte {
	t.Helper()
	sig, err := randao.SignRevealBLS(be.keys[i].priv, BLS_Signer.DomainChainID(), be.epoch, be.peers[i])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

// Through the real anchored committee: the inbox takes the member's BLS reveal
// and refuses one for another epoch, one under the wrong key, and a
// non-member's.
func TestAddInboundReveal_AnchoredBLS(t *testing.T) {
	be := withBLSRevealEpoch(t, "peer-a", "peer-b")

	if err := AddInboundReveal(be.epoch, "peer-a", be.reveal(t, 0)); err != nil {
		t.Fatalf("the member's BLS reveal must be accepted: %v", err)
	}

	resetRevealInbox(t)
	otherEpoch, err := randao.SignRevealBLS(be.keys[0].priv, BLS_Signer.DomainChainID(), be.epoch+1, "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := AddInboundReveal(be.epoch, "peer-a", otherEpoch); !errors.Is(err, ErrRevealDidNotVerify) {
		t.Fatalf("a reveal signed for another epoch must be refused, got %v", err)
	}

	// peer-b's key signing as peer-a's reveal: wrong key for that member.
	wrong, _ := randao.SignRevealBLS(be.keys[1].priv, BLS_Signer.DomainChainID(), be.epoch, "peer-a")
	if err := AddInboundReveal(be.epoch, "peer-a", wrong); !errors.Is(err, ErrRevealDidNotVerify) {
		t.Fatalf("a reveal under another member's key must be refused, got %v", err)
	}
	if err := AddInboundReveal(be.epoch, "peer-outsider", be.reveal(t, 1)); !errors.Is(err, ErrRevealDidNotVerify) {
		t.Fatalf("a non-member must be refused, got %v", err)
	}
}

// A-2 end to end: every member reveals once, the epoch mixes, and a second
// different value for a member cannot be folded.
func TestEntropyAccumulator_BLSEpochMixes(t *testing.T) {
	be := withBLSRevealEpoch(t, "peer-a", "peer-b", "peer-c")
	saved := defaultEntropyAccumulatorStore.accs
	defaultEntropyAccumulatorStore.accs = map[uint64]*randao.Accumulator{}
	t.Cleanup(func() { defaultEntropyAccumulatorStore.accs = saved })

	acc, err := entropyAccumulatorFor(be.epoch)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Expected() != len(be.peers) {
		t.Fatalf("expected set = %d, want the %d anchored members", acc.Expected(), len(be.peers))
	}
	for i, p := range be.peers {
		if err := acc.Fold(uint64(1050+i), p, be.reveal(t, i)); err != nil {
			t.Fatalf("Fold %s: %v", p, err)
		}
	}
	if res := acc.Finalise(); res.Outcome != randao.OutcomeMixed {
		t.Fatalf("fully revealed BLS epoch must mix, got %v", res.Outcome)
	}
}

// Without an anchored pool an epoch has no trustworthy keys: no verifier, no
// accumulator and no reveal, on every node alike.
func TestEpoch_WithoutAnchoringHasNoReveals(t *testing.T) {
	withEntropyCutover(t, 0)
	resetRevealInbox(t)
	resetEntropyAccumulatorStore(t)
	withBeaconEntropy(t, map[uint64][]byte{5: fakeEntropy(0x55, 32)})
	pruneBLSRevealCommitteesBelow(^uint64(0))
	t.Cleanup(func() { pruneBLSRevealCommitteesBelow(^uint64(0)) })

	if _, err := revealVerifierFor(5); !errors.Is(err, ErrBLSRevealNeedsAnchoredPool) {
		t.Fatalf("revealVerifierFor must refuse an unanchored BLS epoch, got %v", err)
	}
	if err := AddInboundReveal(5, "peer-a", make([]byte, randao.BLSRevealLen)); !errors.Is(err, ErrRevealDidNotVerify) {
		t.Fatalf("the inbox must refuse, got %v", err)
	}
	if _, err := entropyAccumulatorFor(5); !errors.Is(err, ErrBLSRevealNeedsAnchoredPool) {
		t.Fatalf("the accumulator must refuse, got %v", err)
	}
}

// The node produces its BLS reveal through the anchored committee, and refuses
// to produce one when its key is not the one the snapshot binds.
func TestProduceRevealForEpoch_AnchoredBLS(t *testing.T) {
	priv, id := newTestIdentity(t)
	withNodeIdentity(t, priv, id)
	be := withBLSRevealEpoch(t, id, "peer-b")

	prev := loadRevealBLSKey
	t.Cleanup(func() { loadRevealBLSKey = prev })
	pub, _ := hex.DecodeString(be.keys[0].pubHex)
	loadRevealBLSKey = func() ([]byte, []byte, error) { return be.keys[0].priv, pub, nil }

	got, err := ProduceRevealForEpoch(be.epoch)
	if err != nil {
		t.Fatalf("seated node must produce its BLS reveal: %v", err)
	}
	if string(got) != string(be.reveal(t, 0)) {
		t.Fatal("produced reveal must be the deterministic BLS reveal")
	}
	if err := AddInboundReveal(be.epoch, id, got); err != nil {
		t.Fatalf("own reveal must pass the inbox: %v", err)
	}

	otherPub, _ := hex.DecodeString(be.keys[1].pubHex)
	loadRevealBLSKey = func() ([]byte, []byte, error) { return be.keys[1].priv, otherPub, nil }
	if _, err := ProduceRevealForEpoch(be.epoch); !errors.Is(err, ErrRevealBLSKeyMismatch) {
		t.Fatalf("a key that is not the snapshot's must be refused, got %v", err)
	}
}

// bn256FieldP is the BN256 base-field prime (kyber pairing/bn256 `p`).
var bn256FieldP, _ = new(big.Int).SetString("65000549695646603732796438742359905742825358107623003571877145026864184071783", 10)

// The re-encoded (x+p) form of a member's reveal is the same point under
// different bytes; the inbox must keep only the canonical one.
func TestAddInboundReveal_BLSEpochRejectsReEncoding(t *testing.T) {
	peers := make([]string, 12)
	for i := range peers {
		peers[i] = fmt.Sprintf("peer-%02d", i)
	}
	be := withBLSRevealEpoch(t, peers...)
	for i, p := range be.peers {
		sig := be.reveal(t, i)
		x := new(big.Int).SetBytes(sig[:32])
		x.Add(x, bn256FieldP)
		if x.BitLen() > 256 {
			continue
		}
		alt := append([]byte(nil), sig...)
		x.FillBytes(alt[:32])
		if err := AddInboundReveal(be.epoch, p, alt); !errors.Is(err, ErrRevealDidNotVerify) {
			t.Fatalf("a re-encoded reveal must be refused, got %v", err)
		}
		if err := AddInboundReveal(be.epoch, p, sig); err != nil {
			t.Fatalf("the canonical reveal must be accepted: %v", err)
		}
		return
	}
	t.Fatal("no member's reveal had room for x+p")
}

// The blocker this removal fixes: an epoch with no anchored pool used to leave
// decideEpoch without a decision, so the next epoch never got entropy and
// committee selection stalled there. It must now enter the fallback, exactly
// like an epoch where a member withheld.
func TestDecideEpoch_UnanchoredEpochEntersFallback(t *testing.T) {
	withEntropyCutover(t, 0)
	resetEntropyAccumulatorStore(t)
	resetFinaliseTracking(t)
	withBeaconEntropy(t, map[uint64][]byte{5: fakeEntropy(0x55, 32)})
	pruneBLSRevealCommitteesBelow(^uint64(0))
	t.Cleanup(func() { pruneBLSRevealCommitteesBelow(^uint64(0)) })

	decideEpoch(5, &config.ZKBlock{BlockNumber: 5*N + RevealCutoffK, Slot: 5*N + RevealCutoffK})

	finaliseTrackMu.Lock()
	_, pending := pendingFallback[5]
	finaliseTrackMu.Unlock()
	if !pending {
		t.Fatal("an unanchored epoch must enter the fallback, not stay undecided")
	}
}

// Stage 1 (no beacon installed) is unchanged: no fallback is started, exactly
// as before BLS reveals.
func TestDecideEpoch_NoBeaconStartsNoFallback(t *testing.T) {
	withEntropyCutover(t, 0)
	resetEntropyAccumulatorStore(t)
	resetFinaliseTracking(t)
	beaconSource = nil
	pruneBLSRevealCommitteesBelow(^uint64(0))
	t.Cleanup(func() { pruneBLSRevealCommitteesBelow(^uint64(0)) })

	decideEpoch(5, &config.ZKBlock{BlockNumber: 5*N + RevealCutoffK, Slot: 5*N + RevealCutoffK})

	finaliseTrackMu.Lock()
	_, pending := pendingFallback[5]
	finaliseTrackMu.Unlock()
	if pending {
		t.Fatal("a Stage-1 node (no beacon) must not start a fallback round")
	}
	if _, err := revealCommitteeSource(5); !errors.Is(err, ErrNoBeaconInstalled) {
		t.Fatalf("no beacon must still report ErrNoBeaconInstalled, got %v", err)
	}
}
