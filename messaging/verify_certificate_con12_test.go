package messaging

// CON-12: the quorum denominator n must be the FLEET-AGREED committee size
// (authenticated snapshot + fleet-uniform cap), NOT reduced by this node's LOCAL
// block_buddy blocklist. A blocklisted member is a non-voter (excluded from the
// numerator) but its seat still counts toward n, so blocking can only make quorum
// HARDER — never lower this node's Byzantine threshold below the fleet's.
//
// Host-gated: the messaging package requires a CGO build; run with
//   CGO_ENABLED=1 go test ./messaging/ -run TestCON12
// The pure denominator/numerator logic is also proven by an isolation harness.

import (
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config/settings"

	"github.com/ethereum/go-ethereum/common"
)

func TestCON12_BlocklistDoesNotShrinkQuorumDenominator(t *testing.T) {
	// Disable the fleet-uniform cap so n is exactly the 7-member committee.
	if settings.IsLoaded() {
		c := settings.Get()
		prev := c.Consensus.MaxValidators
		c.Consensus.MaxValidators = 0
		t.Cleanup(func() { c.Consensus.MaxValidators = prev })
	}

	ids := []string{"p0", "p1", "p2", "p3", "p4", "p5", "p6"}
	members := make([]blsMember, len(ids))
	for i, id := range ids {
		members[i] = mustMintMember(id, byte(0x90+i))
	}
	// Bind the authenticated committee (peer_id -> bls_pub) so votes carry a real,
	// verifying key; install all 7 as the fleet committee.
	useEligibleBound(t, members...)
	// Locally blocklist the last two members (p5, p6).
	withBlockBuddy(t, "p5", "p6")

	hash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000c1")
	votesFrom := func(idx ...int) []BLS_Signer.BLSresponse {
		var v []BLS_Signer.BLSresponse
		for _, i := range idx {
			v = append(v, members[i].blockVote(t, hash.Hex(), 1))
		}
		return v
	}

	// Denominator must be the FULL fleet committee (7), threshold ceil(2*7/3)=5 —
	// NOT the shrunk post-blocklist size 5 (which would give threshold 4).
	res, err := VerifyCertificate(votesFrom(0, 1, 2, 3, 4), hash.Hex(), "", 0)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.CommitteeSize != 7 {
		t.Fatalf("CommitteeSize = %d, want 7 (blocklist must not shrink n)", res.CommitteeSize)
	}
	if res.Threshold != ByzantineQuorum(7) || res.Threshold != 5 {
		t.Fatalf("Threshold = %d, want 5 (ceil(2*7/3))", res.Threshold)
	}
	// 5 non-blocked +1 votes meet the fleet threshold.
	if !res.Reached {
		t.Fatalf("5 votes must reach the fleet quorum of 5, got YesVotes=%d", res.YesVotes)
	}

	// The bug this closes: with a shrunk n=5, threshold would be 4 and these FOUR
	// non-blocked votes would wrongly finalize. They must NOT, since n stays 7.
	res4, err := VerifyCertificate(votesFrom(0, 1, 2, 3), hash.Hex(), "", 0)
	if err != nil {
		t.Fatalf("verify(4): %v", err)
	}
	if res4.CommitteeSize != 7 || res4.Threshold != 5 {
		t.Fatalf("n/threshold drifted: size=%d threshold=%d", res4.CommitteeSize, res4.Threshold)
	}
	if res4.Reached {
		t.Fatalf("4 votes must NOT reach quorum 5 (would only pass under the shrunk-n bug)")
	}

	// A blocklisted member is a NON-VOTER: 4 non-blocked + 1 blocked (p5) counts as
	// 4, not 5, so it must not reach quorum.
	resBlocked, err := VerifyCertificate(votesFrom(0, 1, 2, 3, 5), hash.Hex(), "", 0)
	if err != nil {
		t.Fatalf("verify(blocked): %v", err)
	}
	if resBlocked.YesVotes != 4 {
		t.Fatalf("blocked member's vote must not count: YesVotes=%d, want 4", resBlocked.YesVotes)
	}
	if resBlocked.Reached {
		t.Fatalf("4 counted votes (1 was blocklisted) must not reach quorum 5")
	}
}
