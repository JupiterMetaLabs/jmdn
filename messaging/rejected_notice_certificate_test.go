package messaging

import (
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

	"github.com/ethereum/go-ethereum/common"
)

// A round that misses quorum broadcasts a "rejected" notice to every connected
// peer. Before dissentWithoutSignatures that notice carried the collected +1
// signatures, which is a below-quorum PARTIAL certificate: the only thing
// missing is a response the adversary can produce for itself.
//
// With committees of 7 (config/settings/defaults.go max_validators) the quorum
// is ceil(2*7/3) = 5, so a round that collected 4 and failed hands a seated
// member that withheld its own response exactly the 5th signature it needs.
//
// This test pins both halves: that the unfiltered payload really does complete
// into a valid certificate (the defect), and that what the notice is now
// allowed to carry cannot (the fix).
func TestRejectedNoticePayloadCannotBeCompletedIntoACertificate(t *testing.T) {
	pinTrustSequencer(t, false)
	members := poolOf(t, 7, 7) // n = 7, quorum = ceil(2*7/3) = 5

	hash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000deadbe01")
	const height = 4100

	// What the sequencer collected in a round that missed quorum: 4 yes-votes
	// (one short of 5) plus one explicit dissent. members[4] withheld.
	collected := []BLS_Signer.BLSresponse{
		members[0].blockVoteAt(t, hash.Hex(), 1, height),
		members[1].blockVoteAt(t, hash.Hex(), 1, height),
		members[2].blockVoteAt(t, hash.Hex(), 1, height),
		members[3].blockVoteAt(t, hash.Hex(), 1, height),
		members[5].blockVoteAt(t, hash.Hex(), -1, height),
	}
	withheld := members[4].blockVoteAt(t, hash.Hex(), 1, height)

	// Precondition: the round genuinely failed.
	if res, err := VerifyCertificate(collected, hash.Hex(), testBlockConsensusHash, height); err != nil || res.Reached {
		t.Fatalf("precondition: the collected set must be below quorum, got %+v err=%v", res, err)
	}

	// The defect: the raw collected set plus the withheld response certifies a
	// block the fleet never committed. If this ever stops being true the test
	// is no longer pinning anything and should be revisited, not deleted.
	forged := append(append([]BLS_Signer.BLSresponse{}, collected...), withheld)
	res, err := VerifyCertificate(forged, hash.Hex(), testBlockConsensusHash, height)
	if err != nil {
		t.Fatalf("verify forged certificate: %v", err)
	}
	if !res.Reached {
		t.Fatalf("expected the unfiltered rejected-notice payload + the withheld response to reach quorum "+
			"(that is the defect the filter removes), got %+v", res)
	}

	// The fix: what the rejected notice may now carry.
	onWire := dissentWithoutSignatures(collected)
	for _, r := range onWire {
		if r.Agree {
			t.Fatalf("a rejected notice must carry no +1 entries, got %+v", r)
		}
		if r.Signature != "" {
			t.Fatalf("a rejected notice must carry no signature material, got %+v", r)
		}
	}

	completed := append(append([]BLS_Signer.BLSresponse{}, onWire...), withheld)
	if res, err := VerifyCertificate(completed, hash.Hex(), testBlockConsensusHash, height); err != nil || res.Reached {
		t.Fatalf("the filtered rejected notice must not be completable into a certificate, got %+v err=%v", res, err)
	}
}

// The sequencer reuses the same slice after the broadcast for its own alerting
// and reputation classification, both of which read Signature
// (reputation.ShouldChargeBadSignature). Stripping in place would silently
// reclassify every dissenting member as an abstain.
func TestDissentWithoutSignaturesDoesNotMutateItsInput(t *testing.T) {
	in := []BLS_Signer.BLSresponse{
		{PeerID: "a", Agree: true, Signature: "sig-a"},
		{PeerID: "b", Agree: false, Signature: "sig-b", RejectionReasons: map[string]string{"b": "behind"}},
	}

	out := dissentWithoutSignatures(in)

	if len(in) != 2 || in[0].Signature != "sig-a" || in[1].Signature != "sig-b" {
		t.Fatalf("input slice was modified: %+v", in)
	}
	if len(out) != 1 || out[0].PeerID != "b" {
		t.Fatalf("want only the dissent entry, got %+v", out)
	}
	if out[0].Signature != "" {
		t.Fatalf("dissent signature must be cleared, got %q", out[0].Signature)
	}
	if out[0].RejectionReasons["b"] != "behind" {
		t.Fatalf("rejection reasons must survive, got %+v", out[0].RejectionReasons)
	}
}

// An all-dissent round leaves nothing signature-bearing to send; an all-agree
// round that still missed quorum (responses lost in flight) must send nothing
// at all rather than a partial certificate.
func TestDissentWithoutSignaturesEdgeCases(t *testing.T) {
	allAgree := []BLS_Signer.BLSresponse{
		{PeerID: "a", Agree: true, Signature: "sig-a"},
		{PeerID: "b", Agree: true, Signature: "sig-b"},
	}
	if got := dissentWithoutSignatures(allAgree); len(got) != 0 {
		t.Fatalf("an all-agree failed round must put nothing on the wire, got %+v", got)
	}
	if got := dissentWithoutSignatures(nil); len(got) != 0 {
		t.Fatalf("nil input must yield nothing, got %+v", got)
	}
}
