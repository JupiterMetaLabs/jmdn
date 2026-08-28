package messaging

// Tests for M0 (Architecture §7.1c) - required test list:
//  1. a failed round advances period+1 and draws a DIFFERENT committee
//  2. a node holding only the latest certificate can verify the whole prefix
//     without having seen the intermediate ones
//  3. the double-vote case is excluded from both tallies

import (
	"testing"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"

	"github.com/JupiterMetaLabs/avc/committee"
)

const testHeight = uint64(100)

// keypair is a test-only convenience: an ephemeral BLS key plus the voter ID
// it signs as.
type keypair struct {
	id   string
	priv []byte
	pub  []byte
}

func newKeypairs(t *testing.T, n int) []keypair {
	t.Helper()
	kps := make([]keypair, n)
	for i := range kps {
		priv, pub, err := blssign.GenerateBLSKeyPair()
		if err != nil {
			t.Fatalf("keygen %d: %v", i, err)
		}
		kps[i] = keypair{id: "peer-" + string(rune('A'+i)), priv: priv, pub: pub}
	}
	return kps
}

func pubKeyMap(kps []keypair) map[string][]byte {
	m := make(map[string][]byte, len(kps))
	for _, k := range kps {
		m[k.id] = k.pub
	}
	return m
}

// buildCertificate signs a TimeoutVote from every keypair and tallies them
// into a certificate - a small end-to-end helper shared by several tests.
func buildCertificate(t *testing.T, kps []keypair, height, period uint64) *TimeoutCertificate {
	t.Helper()
	votes := make([]TimeoutVote, len(kps))
	for i, k := range kps {
		v, err := SignTimeoutVote(k.priv, k.id, BLS_Signer.DomainChainID(), height, period)
		if err != nil {
			t.Fatalf("sign vote for %s: %v", k.id, err)
		}
		votes[i] = v
	}
	cert, ok, err := TallyTimeoutVotes(votes, height, period, len(kps), pubKeyMap(kps), nil)
	if err != nil {
		t.Fatalf("tally: %v", err)
	}
	if !ok {
		t.Fatal("tally did not reach quorum with all voters present")
	}
	return cert
}

// TestFailedRoundAdvancesPeriodAndChangesCommittee is required test #1: a
// certified timeout must both advance the period and, fed into the real
// committee-selection seed, draw a different committee than period 0 did.
func TestFailedRoundAdvancesPeriodAndChangesCommittee(t *testing.T) {
	kps := newKeypairs(t, 4) // n=4, quorum=3 (ceil(2*4/3))
	store := NewPeriodStore()

	if got := store.PeriodFor(testHeight); got != 0 {
		t.Fatalf("fresh height should start at period 0, got %d", got)
	}

	cert := buildCertificate(t, kps, testHeight, 1)
	newPeriod, accepted, err := store.AcceptTimeoutCertificate(*cert, len(kps), pubKeyMap(kps))
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !accepted || newPeriod != 1 {
		t.Fatalf("expected acceptance advancing to period 1, got accepted=%v period=%d", accepted, newPeriod)
	}

	// Now prove this isn't just bookkeeping: feed both periods into the real
	// A-ExpJ seed/select path and confirm the committees actually differ.
	// A large pool with k well below n: with only 6 members and k=3 there is
	// a non-negligible (~5%) chance two independent seeds land on the same
	// 3-subset by pure luck, which would make this test flaky rather than
	// wrong. At n=20, k=5 an accidental collision is astronomically unlikely
	// (1-in-15,504), so a mismatch here reliably means period isn't reaching
	// the seed, not bad luck.
	salt := committee.SaltSource{Salt: []byte("test-network-salt")}
	members := make([]committee.Member, 20)
	for i := range members {
		members[i] = committee.Member{PeerID: "v" + string(rune('A'+i)), Weight: 1}
	}
	snap := committee.Snapshot{Epoch: 0, Members: members}
	prevHash := []byte("some-parent-block-hash-32-bytes")

	seedAtPeriod0, err := committee.DeriveSeed(salt, committee.SeedInput{EntropyEpoch: 0, PrevHash: prevHash, Height: testHeight, Period: 0})
	if err != nil {
		t.Fatalf("derive seed period 0: %v", err)
	}
	seedAtPeriod1, err := committee.DeriveSeed(salt, committee.SeedInput{EntropyEpoch: 0, PrevHash: prevHash, Height: testHeight, Period: newPeriod})
	if err != nil {
		t.Fatalf("derive seed period 1: %v", err)
	}

	committeeAt0, err := committee.CommitteeFor(seedAtPeriod0, snap, 5)
	if err != nil {
		t.Fatalf("committee at period 0: %v", err)
	}
	committeeAt1, err := committee.CommitteeFor(seedAtPeriod1, snap, 5)
	if err != nil {
		t.Fatalf("committee at period 1: %v", err)
	}

	if sameMembers(committeeAt0, committeeAt1) {
		t.Fatal("timing out and advancing the period re-drew the SAME committee - a stalled committee would stall forever")
	}
}

func sameMembers(a, b []committee.Member) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, m := range a {
		seen[m.PeerID] = true
	}
	for _, m := range b {
		if !seen[m.PeerID] {
			return false
		}
	}
	return true
}

// TestSyncingNodeAcceptsLatestCertificateWithoutIntermediates is required
// test #2: a node that never observed certificates for periods 1-4 must
// still accept a certificate for period 5 outright, chaining only on
// PrevIndex (an index reference), never on having the prior certificates in
// hand.
func TestSyncingNodeAcceptsLatestCertificateWithoutIntermediates(t *testing.T) {
	kps := newKeypairs(t, 4)
	store := NewPeriodStore() // fresh - has NEVER seen periods 1..4 for this height

	cert := buildCertificate(t, kps, testHeight, 5)

	newPeriod, accepted, err := store.AcceptTimeoutCertificate(*cert, len(kps), pubKeyMap(kps))
	if err != nil {
		t.Fatalf("a syncing node should accept the latest certificate outright: %v", err)
	}
	if !accepted || newPeriod != 5 {
		t.Fatalf("expected acceptance jumping straight to period 5, got accepted=%v period=%d", accepted, newPeriod)
	}

	// A stale re-delivery of the same certificate must be a no-op, not an error.
	newPeriod, accepted, err = store.AcceptTimeoutCertificate(*cert, len(kps), pubKeyMap(kps))
	if err != nil {
		t.Fatalf("re-delivering the same certificate should not error: %v", err)
	}
	if accepted || newPeriod != 5 {
		t.Fatalf("re-delivered certificate should be a no-op at the already-known period, got accepted=%v period=%d", accepted, newPeriod)
	}
}

// TestSelfInconsistentCertificateRejected pins §7.1c point 1's self-
// consistency rule directly: a certificate whose Period does not follow its
// own PrevIndex is not a valid timeout proof, regardless of signatures.
func TestSelfInconsistentCertificateRejected(t *testing.T) {
	kps := newKeypairs(t, 4)
	cert := buildCertificate(t, kps, testHeight, 5)
	cert.PrevIndex = 2 // tamper: claims to follow period 2, not 4

	if ok, err := VerifyTimeoutCertificate(*cert, len(kps), pubKeyMap(kps)); ok || err == nil {
		t.Fatal("a certificate with an inconsistent PrevIndex/Period pair was accepted")
	}
}

// TestDoubleVoteExcludedFromBothTallies is required test #3: a peer that
// cast both a block-vote and a TimeoutVote for the same (height, period)
// must be detected and, once excluded, must not count toward the TimeoutVote
// quorum either.
func TestDoubleVoteExcludedFromBothTallies(t *testing.T) {
	kps := newKeypairs(t, 4) // quorum = 3 of 4

	blockVoters := map[string]bool{kps[0].id: true}
	timeoutVoters := map[string]bool{kps[0].id: true, kps[1].id: true, kps[2].id: true, kps[3].id: true}

	equivocators := DetectTimeoutBlockVoteEquivocation(blockVoters, timeoutVoters)
	if len(equivocators) != 1 || equivocators[0] != kps[0].id {
		t.Fatalf("expected exactly [%s] flagged, got %v", kps[0].id, equivocators)
	}

	excluded := make(map[string]bool, len(equivocators))
	for _, id := range equivocators {
		excluded[id] = true
	}

	votes := make([]TimeoutVote, len(kps))
	for i, k := range kps {
		v, err := SignTimeoutVote(k.priv, k.id, BLS_Signer.DomainChainID(), testHeight, 1)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		votes[i] = v
	}

	cert, ok, err := TallyTimeoutVotes(votes, testHeight, 1, len(kps), pubKeyMap(kps), excluded)
	if err != nil {
		t.Fatalf("tally: %v", err)
	}
	if !ok {
		t.Fatal("expected quorum still reached with 3 of 4 valid voters (excluding the equivocator)")
	}
	for _, signer := range cert.SignerBitmap {
		if signer == kps[0].id {
			t.Fatalf("equivocating peer %s was still counted in the certificate", kps[0].id)
		}
	}
	if len(cert.SignerBitmap) != 3 {
		t.Fatalf("expected exactly 3 signers (excluding the equivocator), got %d: %v", len(cert.SignerBitmap), cert.SignerBitmap)
	}
}

// TestRoundContextForBlockReadsPeriodStore confirms the unstub: a block at a
// height with a certified timeout now reports the derived period instead of
// the old hardcoded 0.
func TestRoundContextForBlockReadsPeriodStore(t *testing.T) {
	kps := newKeypairs(t, 4)
	saved := DefaultPeriodStore
	DefaultPeriodStore = NewPeriodStore()
	t.Cleanup(func() { DefaultPeriodStore = saved })

	cert := buildCertificate(t, kps, testHeight, 1)
	if _, accepted, err := DefaultPeriodStore.AcceptTimeoutCertificate(*cert, len(kps), pubKeyMap(kps)); err != nil || !accepted {
		t.Fatalf("accept: accepted=%v err=%v", accepted, err)
	}

	block := &config.ZKBlock{BlockNumber: testHeight}
	ctx := RoundContextForBlock(block)
	if ctx.Period != 1 {
		t.Fatalf("RoundContextForBlock.Period = %d, want 1 (still reading the hardcoded 0?)", ctx.Period)
	}

	// A height with no certificate must still read back 0 - the common case
	// where a round never times out must be unaffected.
	other := &config.ZKBlock{BlockNumber: testHeight + 1}
	if ctx := RoundContextForBlock(other); ctx.Period != 0 {
		t.Fatalf("a height with no certificate should read Period=0, got %d", ctx.Period)
	}
}
