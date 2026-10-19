package messaging

import (
	"errors"
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/settings"

	"github.com/ethereum/go-ethereum/common"
)

// poolOf mints n bound members (peer_id ↔ bls_pub, like the seed snapshot) and
// pins consensus.max_validators to k so the legacy verifier caps the committee
// to the alphabetically-first k of the pool.
func poolOf(t *testing.T, n, k int) []blsMember {
	t.Helper()
	c := settings.Get()
	prev := c.Consensus.MaxValidators
	c.Consensus.MaxValidators = k
	t.Cleanup(func() { c.Consensus.MaxValidators = prev })
	members := make([]blsMember, n)
	for i := 0; i < n; i++ {
		members[i] = mustMintMember("p"+itoa(i), byte(0x40+i))
	}
	useEligibleBound(t, members...)
	return members
}

// The 2026-10-15 shape: a block sealed by a seven the seed drew from a pool of
// ten, catching up on a node whose legacy verifier only counts the
// alphabetically-first seven (p0..p6). Five of the sealing seven are outside
// that prefix, so the legacy tally is 2/5 — and before this change that block
// could never be applied on this node (all eight WAN nodes at 2127, node-1 at
// 1761). Pool-wide, the same certificate is 5 distinct registered validators
// over k=7 → threshold 5 → quorum.
func TestVerifySyncedBlockCertificate_PoolWideFallbackAcceptsSealingCommitteeOutsideLegacyCap(t *testing.T) {
	enableV2(t)
	pinTrustSequencer(t, false)
	members := poolOf(t, 10, 7)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000002128")
	const height = 2128
	// Sealing committee: p5..p9 signed (p5, p6 inside the legacy cap; p7, p8, p9 outside).
	var votes []BLS_Signer.BLSresponse
	for i := 5; i < 10; i++ {
		votes = append(votes, members[i].blockVoteAt(t, hash.Hex(), 1, height))
	}
	block := &config.ZKBlock{BlockNumber: height, BlockHash: hash, ConsensusHash: common.HexToHash(testBlockConsensusHash)}

	legacy, err := VerifyCertificate(votes, hash.Hex(), testBlockConsensusHash, height)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Reached || legacy.YesVotes != 2 || legacy.CommitteeSize != 7 {
		t.Fatalf("precondition: legacy verifier must count only the alphabetical cap: got %+v", legacy)
	}

	got, err := VerifySyncedBlockCertificate(block, votes)
	if err != nil {
		t.Fatalf("synced verify: %v", err)
	}
	if !got.Reached {
		t.Fatalf("pool-wide fallback must reach quorum with 5 distinct pool signers over k=7: %+v", got)
	}
	if got.YesVotes != 5 || got.CommitteeSize != 7 || got.Threshold != 5 {
		t.Fatalf("pool-wide result = %+v, want yes=5 size=7 threshold=5", got)
	}
}

// Four pool signers over k=7 is below ceil(2·7/3)=5 on every tally: the
// fallback must NOT lower the bar, and the returned numbers are the legacy
// verifier's (what the operator sees in the apply error).
func TestVerifySyncedBlockCertificate_PoolWideFallbackKeepsThreshold(t *testing.T) {
	enableV2(t)
	pinTrustSequencer(t, false)
	members := poolOf(t, 10, 7)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000002129")
	const height = 2129
	var votes []BLS_Signer.BLSresponse
	for i := 6; i < 10; i++ {
		votes = append(votes, members[i].blockVoteAt(t, hash.Hex(), 1, height))
	}
	got, err := VerifySyncedBlockCertificate(&config.ZKBlock{BlockNumber: height, BlockHash: hash, ConsensusHash: common.HexToHash(testBlockConsensusHash)}, votes)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reached {
		t.Fatalf("4 signers must not reach a 5-of-7 quorum on any tally: %+v", got)
	}
	if got.CommitteeSize != 7 || got.Threshold != 5 {
		t.Fatalf("below-quorum result must report the legacy committee: %+v", got)
	}
}

// Signers outside the fleet-eligible pool never count, on any tally: five
// valid block-bound signatures from unregistered keys stay at 0.
func TestVerifySyncedBlockCertificate_PoolWideFallbackIgnoresNonPoolSigners(t *testing.T) {
	enableV2(t)
	pinTrustSequencer(t, false)
	_ = poolOf(t, 10, 7)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000002130")
	const height = 2130
	var votes []BLS_Signer.BLSresponse
	for i := 0; i < 5; i++ {
		outsider := mustMintMember("zz-outsider-"+itoa(i), byte(0xC0+i))
		votes = append(votes, outsider.blockVoteAt(t, hash.Hex(), 1, height))
	}
	got, err := VerifySyncedBlockCertificate(&config.ZKBlock{BlockNumber: height, BlockHash: hash, ConsensusHash: common.HexToHash(testBlockConsensusHash)}, votes)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reached || got.YesVotes != 0 {
		t.Fatalf("outsider signatures must not count: %+v", got)
	}
}

// A pool member voting with a key other than its bound bls_pub does not count
// pool-wide either (same binding rule as the live verifier).
func TestVerifySyncedBlockCertificate_PoolWideFallbackEnforcesKeyBinding(t *testing.T) {
	enableV2(t)
	pinTrustSequencer(t, false)
	members := poolOf(t, 10, 7)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000002131")
	const height = 2131
	var votes []BLS_Signer.BLSresponse
	for i := 5; i < 9; i++ {
		votes = append(votes, members[i].blockVoteAt(t, hash.Hex(), 1, height))
	}
	// Fifth signer: p9's peer_id, but signed with p0's key.
	forged := members[0].blockVoteAt(t, hash.Hex(), 1, height)
	forged.PeerID = members[9].peerID
	votes = append(votes, forged)
	got, err := VerifySyncedBlockCertificate(&config.ZKBlock{BlockNumber: height, BlockHash: hash, ConsensusHash: common.HexToHash(testBlockConsensusHash)}, votes)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reached {
		t.Fatalf("a mis-bound key must not complete the quorum: %+v", got)
	}
}

// When the legacy verifier already reaches quorum the result is byte-identical
// to before (the fallbacks are never consulted).
func TestVerifySyncedBlockCertificate_LegacyPassUnchanged(t *testing.T) {
	enableV2(t)
	pinTrustSequencer(t, false)
	members := poolOf(t, 10, 7)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000002132")
	const height = 2132
	var votes []BLS_Signer.BLSresponse
	for i := 0; i < 5; i++ {
		votes = append(votes, members[i].blockVoteAt(t, hash.Hex(), 1, height))
	}
	want, wantErr := VerifyCertificate(votes, hash.Hex(), testBlockConsensusHash, height)
	got, gotErr := VerifySyncedBlockCertificate(&config.ZKBlock{BlockNumber: height, BlockHash: hash, ConsensusHash: common.HexToHash(testBlockConsensusHash)}, votes)
	if (wantErr == nil) != (gotErr == nil) || want != got {
		t.Fatalf("legacy-pass path diverged: want (%+v, %v), got (%+v, %v)", want, wantErr, got, gotErr)
	}
	if !got.Reached {
		t.Fatalf("5 of the alphabetical seven must reach quorum: %+v", got)
	}
}

// ── trust_sequencer_certificate (testnet stopgap) ───────────────────────────────

func trustSequencer(t *testing.T) { pinTrustSequencer(t, true) }

// pinTrustSequencer fixes TrustSequencerCertificate for one test. Its compiled
// default is ON (testnet), so a test that is about the real quorum check must
// pin it OFF; otherwise one valid vote passes and the test proves nothing.
func pinTrustSequencer(t *testing.T, on bool) {
	t.Helper()
	prev := TrustSequencerCertificate
	TrustSequencerCertificate = on
	t.Cleanup(func() { TrustSequencerCertificate = prev })
}

// With the flag on, a below-quorum certificate is accepted only with f+1
// verifying block-bound +1 votes from eligible validators (k=7 → f=2 → 3). One
// or two keys cannot finalize a block, and a verifier error is never relaxed.
func TestTrustSequencerCertificate_RequiresFPlusOneVotes(t *testing.T) {
	enableV2(t)
	trustSequencer(t)
	members := poolOf(t, 10, 7)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000002140")
	const height = 2140
	block := &config.ZKBlock{BlockNumber: height, BlockHash: hash, ConsensusHash: common.HexToHash(testBlockConsensusHash)}

	var votes []BLS_Signer.BLSresponse
	for i := 7; i < 10; i++ { // signers outside the legacy alphabetical cap
		votes = append(votes, members[i].blockVoteAt(t, hash.Hex(), 1, height))
	}

	for n := 1; n <= 2; n++ {
		got, err := VerifySyncedBlockCertificate(block, votes[:n])
		if err != nil {
			t.Fatal(err)
		}
		if got.Reached {
			t.Fatalf("%d vote(s) must not be accepted (need f+1=3): %+v", n, got)
		}
	}
	got, err := VerifySyncedBlockCertificate(block, votes)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Reached || got.YesVotes != 3 {
		t.Fatalf("f+1=3 verifying votes must be accepted in trusted mode: %+v", got)
	}
	// The same helper the live receive path uses, fed a verifier error (e.g.
	// round context unavailable): still accepted on the one real vote.
	relaxed, rerr := relaxForTrustedSequencer("block_propagation", CertificateResult{}, errVerifierUnavailable, votes, hash.Hex(), testBlockConsensusHash, height)
	if rerr != nil || !relaxed.Reached {
		t.Fatalf("verifier error must be overridden by a real registered vote: (%+v, %v)", relaxed, rerr)
	}
}

// Trust is extended to the sequencer's DECISION, not to any block anyone sends:
// a certificate with no verifying registered vote (outsider keys, a -1 vote, or
// a vote for another block) is still rejected.
func TestTrustSequencerCertificate_NoRegisteredVoteStillRejects(t *testing.T) {
	enableV2(t)
	trustSequencer(t)
	members := poolOf(t, 10, 7)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000002141")
	other := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000002142")
	const height = 2141
	outsider := mustMintMember("zz-outsider", 0xEE)
	votes := []BLS_Signer.BLSresponse{
		outsider.blockVoteAt(t, hash.Hex(), 1, height),    // not registered
		members[3].blockVoteAt(t, hash.Hex(), -1, height), // registered, but a NO
		members[4].blockVoteAt(t, other.Hex(), 1, height), // registered, wrong block
	}
	got, err := VerifySyncedBlockCertificate(&config.ZKBlock{BlockNumber: height, BlockHash: hash, ConsensusHash: common.HexToHash(testBlockConsensusHash)}, votes)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reached {
		t.Fatalf("no verifying registered +1 for this block must stay rejected even in trusted mode: %+v", got)
	}
	relaxed, rerr := relaxForTrustedSequencer("thebesync", CertificateResult{}, errVerifierUnavailable, votes, hash.Hex(), testBlockConsensusHash, height)
	if rerr == nil || relaxed.Reached {
		t.Fatalf("a verifier error with no real vote must be preserved: (%+v, %v)", relaxed, rerr)
	}
}

// errVerifierUnavailable stands in for any receive-path verifier error (for
// example an unavailable committee source) that relaxForTrustedSequencer may
// override.
var errVerifierUnavailable = errors.New("committee: verifier unavailable")

// Flag off: relaxForTrustedSequencer is a pass-through, byte for byte.
func TestTrustSequencerCertificate_OffIsPassThrough(t *testing.T) {
	prev := TrustSequencerCertificate
	TrustSequencerCertificate = false
	t.Cleanup(func() { TrustSequencerCertificate = prev })
	in := CertificateResult{CommitteeSize: 7, Threshold: 5, YesVotes: 3, Reached: false}
	got, err := relaxForTrustedSequencer("x", in, nil, nil, "", "", 1)
	if err != nil || got != in {
		t.Fatalf("flag off must not alter the verifier result: (%+v, %v)", got, err)
	}
	got, err = relaxForTrustedSequencer("x", CertificateResult{}, errVerifierUnavailable, nil, "", "", 1)
	if err == nil || got.Reached {
		t.Fatalf("flag off must not swallow verifier errors: (%+v, %v)", got, err)
	}
}

// The gate is a pure function of whether committee anchoring is configured.
// This is the deterministic check: it does not depend on any seat draw.
func TestPoolWideSyncFallbackAllowed_FollowsAnchoringConfig(t *testing.T) {
	withAnchorSettings(t, 0, 20)
	if !poolWideSyncFallbackAllowed() {
		t.Fatal("anchoring off: the pool-wide bridge must stay available (testnet behaviour is unchanged)")
	}
	withAnchorSettings(t, 20, 20)
	if poolWideSyncFallbackAllowed() {
		t.Fatal("anchoring configured: the pool-wide bridge must be closed")
	}
	withAnchorSettings(t, 20, 0)
	if !poolWideSyncFallbackAllowed() {
		t.Fatal("a zero epoch length leaves anchoring off, so the bridge stays available")
	}
}

// End to end at an UNANCHORED height (activation far above it), so the anchored
// branch of the verifier is not taken and only the fallbacks differ. Turning
// anchoring on must leave exactly the legacy-then-seated result: the pool-wide
// tally may not add acceptance.
//
// The expected value is computed independently from the same two checks the
// verifier runs before the fallback, so the test never depends on which voters
// the seat draw happens to favour: no skip, no coincidence. If those two checks
// already accept the votes, the gate is not exercised by this case and the test
// says so in its log; the predicate test above still covers the gate.
func TestVerifySyncedBlockCertificate_PoolWideFallbackClosedOnceAnchoringConfigured(t *testing.T) {
	enableV2(t)
	pinTrustSequencer(t, false)
	members := poolOf(t, 10, 7)
	const height = 2150
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000002150")
	testBlockConsensusHash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000f0f").Hex()
	block := &config.ZKBlock{BlockNumber: height, BlockHash: hash, ConsensusHash: common.HexToHash(testBlockConsensusHash)}

	// p5..p9: outside the legacy alphabetical cap (p0..p6) for four of five signers.
	var votes []BLS_Signer.BLSresponse
	for i := 5; i < 10; i++ {
		votes = append(votes, members[i].blockVoteAt(t, hash.Hex(), 1, height))
	}

	// Anchoring off (default settings): the existing bridge behaviour.
	off, err := VerifySyncedBlockCertificate(block, votes)
	if err != nil || !off.Reached {
		t.Fatalf("precondition: with anchoring off these five pool signers must be accepted: (%+v, %v)", off, err)
	}

	// Anchoring configured, activation far above this height.
	withAnchorSettings(t, 5000, 20)
	if periodIsAnchored(EpochForHeight(height)) {
		t.Fatal("test setup: height must stay below the anchored start")
	}

	legacy, lerr := VerifyCertificate(votes, hash.Hex(), testBlockConsensusHash, height)
	want, werr := legacy, lerr
	rc := RoundContextForBlock(block)
	if seated, serr := VerifyCertificateForRound(votes, hash.Hex(), testBlockConsensusHash, height, rc); serr == nil && seated.Reached {
		want, werr = seated, nil
	}

	got, gerr := VerifySyncedBlockCertificate(block, votes)
	if (werr == nil) != (gerr == nil) || got != want {
		t.Fatalf("with anchoring configured the result must be exactly legacy-then-seated: want (%+v, %v), got (%+v, %v)", want, werr, got, gerr)
	}
	if !want.Reached {
		if got.Reached {
			t.Fatalf("pool-wide fallback accepted with anchoring configured: %+v", got)
		}
	} else {
		t.Logf("the legacy or seated check already accepts these votes at this height; the gate was not exercised by this case (see the predicate test)")
	}
}
