package messaging

import (
	"encoding/hex"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/JupiterMetaLabs/avc/committee"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/metrics"
)

// consensus_legacy_votes_total is a DETECTOR: with reject_legacy_votes on it
// must count a legacy (unbound) signature from a validator's OWN key and then
// refuse the vote — and it must NOT count a forged response that names a
// seated peer_id but carries a foreign key, because BLS_Verifier.Verify checks
// the signature against the pubkey inside the response. Anyone who can submit
// a certificate could otherwise make the metric blame an innocent validator.

func legacySiteCount(t *testing.T, site string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.ConsensusLegacyVotesTotal.WithLabelValues(site))
}

func TestLegacyVoteDetector_PoolTally_CountsOwnKeyRefusesForeignKey(t *testing.T) {
	prev := RejectLegacyVotes
	RejectLegacyVotes = true
	defer func() { RejectLegacyVotes = prev }()

	validator := mustMintMember("peerV", 0x31)
	attacker := mustMintMember("peerX", 0x32)
	// Snapshot-bound committee: peerV is bound to validator's key.
	committeeMap := map[string]string{validator.peerID: normalizeBLSPub(validator.pubHex)}
	blockHashHex := "0x" + hex.EncodeToString(make([]byte, 32))

	// 1. The validator itself signs the legacy domain: detected, refused.
	before := legacySiteCount(t, metrics.LegacyVoteSitePoolTally)
	own := validator.legacyVote(t, 1)
	if yes := countEligibleYes([]BLS_Signer.BLSresponse{own}, blockHashHex, testBlockConsensusHash, 7, committeeMap, true); yes != 0 {
		t.Fatalf("legacy vote must be REFUSED with reject_legacy_votes on, counted yes=%d", yes)
	}
	if got := legacySiteCount(t, metrics.LegacyVoteSitePoolTally) - before; got != 1 {
		t.Fatalf("own-key legacy vote: detector delta=%v, want 1", got)
	}

	// 2. Forged: seated peer_id, attacker's key, valid legacy signature under
	//    that key. Verify(resp) alone would pass — the binding must stop it.
	before = legacySiteCount(t, metrics.LegacyVoteSitePoolTally)
	forged := attacker.legacyVote(t, 1)
	forged.PeerID = validator.peerID
	if yes := countEligibleYes([]BLS_Signer.BLSresponse{forged}, blockHashHex, testBlockConsensusHash, 7, committeeMap, true); yes != 0 {
		t.Fatalf("forged vote must be refused, counted yes=%d", yes)
	}
	if got := legacySiteCount(t, metrics.LegacyVoteSitePoolTally) - before; got != 0 {
		t.Fatalf("forged legacy vote with a foreign key must NOT be detected, delta=%v", got)
	}

	// 3. Unknown peer_id: not even classified (bounded cost).
	before = legacySiteCount(t, metrics.LegacyVoteSitePoolTally)
	stranger := attacker.legacyVote(t, 1)
	countEligibleYes([]BLS_Signer.BLSresponse{stranger}, blockHashHex, testBlockConsensusHash, 7, committeeMap, true)
	if got := legacySiteCount(t, metrics.LegacyVoteSitePoolTally) - before; got != 0 {
		t.Fatalf("non-member legacy vote must not be detected, delta=%v", got)
	}

	// 4. Legacy committee source (no bound key): id-only, like acceptance.
	before = legacySiteCount(t, metrics.LegacyVoteSitePoolTally)
	unbound := map[string]string{validator.peerID: ""}
	countEligibleYes([]BLS_Signer.BLSresponse{own}, blockHashHex, testBlockConsensusHash, 7, unbound, true)
	if got := legacySiteCount(t, metrics.LegacyVoteSitePoolTally) - before; got != 1 {
		t.Fatalf("unbound snapshot: own legacy vote should be detected by id, delta=%v", got)
	}
}

func TestLegacyVoteDetector_CommitteeV2_CountsOwnKeyRefusesForeignKey(t *testing.T) {
	prev := RejectLegacyVotes
	RejectLegacyVotes = true
	defer func() { RejectLegacyVotes = prev }()

	validator := mustMintMember("peerV", 0x33)
	attacker := mustMintMember("peerX", 0x34)
	vPub, err := hex.DecodeString(validator.pubHex)
	if err != nil {
		t.Fatal(err)
	}
	seated := []committee.Member{{PeerID: validator.peerID, BLSPub: vPub, Weight: 1}}
	blockHashHex := "0x" + hex.EncodeToString(make([]byte, 32))

	mkVerifier := func(resps ...BLS_Signer.BLSresponse) blsVerifier {
		orig := make(map[string]BLS_Signer.BLSresponse, len(resps))
		for _, r := range resps {
			sig, err := hex.DecodeString(r.Signature)
			if err != nil {
				t.Fatal(err)
			}
			orig[voteKey(r.PeerID, blsKeyBytes(r.PubKey), sig)] = r
		}
		return blsVerifier{bindings: blockHashHex, consensusHash: testBlockConsensusHash, orig: orig, seated: seatedPeerKeys(seated)}
	}
	toVote := func(r BLS_Signer.BLSresponse) committee.Vote {
		sig, _ := hex.DecodeString(r.Signature)
		pub, _ := hex.DecodeString(r.PubKey)
		return committee.Vote{PeerID: r.PeerID, BLSPub: pub, Approve: r.Agree, Signature: sig}
	}

	// Own key: detected, still refused.
	own := validator.legacyVote(t, 1)
	before := legacySiteCount(t, metrics.LegacyVoteSiteCommitteeV2)
	if mkVerifier(own).VerifyVote(toVote(own), BLS_Signer.DomainChainID(), 7, nil) {
		t.Fatal("legacy vote must not verify with reject_legacy_votes on")
	}
	if got := legacySiteCount(t, metrics.LegacyVoteSiteCommitteeV2) - before; got != 1 {
		t.Fatalf("own-key legacy vote: detector delta=%v, want 1", got)
	}

	// Foreign key under the seated peer_id: refused and NOT detected.
	forged := attacker.legacyVote(t, 1)
	forged.PeerID = validator.peerID
	before = legacySiteCount(t, metrics.LegacyVoteSiteCommitteeV2)
	if mkVerifier(forged).VerifyVote(toVote(forged), BLS_Signer.DomainChainID(), 7, nil) {
		t.Fatal("forged vote must not verify")
	}
	if got := legacySiteCount(t, metrics.LegacyVoteSiteCommitteeV2) - before; got != 0 {
		t.Fatalf("forged legacy vote with a foreign key must NOT be detected, delta=%v", got)
	}
}
