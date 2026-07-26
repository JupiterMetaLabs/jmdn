package messaging

// ONE authenticated verifier, ONE threshold: Byzantine 2f+1 over the
// authenticated committee size (f = floor((n-1)/3)), NEVER a simple majority and
// NEVER derived from the number of votes received.
//
// These fail on the earlier code (which used (validTotal/2)+1 in
// ProcessBlockLocally and (MaxMainPeers/2)+1 elsewhere) and pass once every
// path routes through VerifyCertificate.

import (
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/settings"

	"github.com/ethereum/go-ethereum/common"
)

// committeeOfSize returns n distinct BLS members with peer_ids p0..p{n-1} and
// installs them as the eligible committee for the test.
func committeeOfSize(t *testing.T, n int) []blsMember {
	t.Helper()
	// These scenarios verify 2f+1 over the FULL committee of size n. If settings
	// happen to be loaded (by another test) with the production max_validators cap
	// (default 5), eligibleMembers would trim n>5 committees and break the
	// threshold math. Disable the cap for the test's duration; restore after.
	if settings.IsLoaded() {
		c := settings.Get()
		prev := c.Consensus.MaxValidators
		c.Consensus.MaxValidators = 0
		t.Cleanup(func() { c.Consensus.MaxValidators = prev })
	}
	members := make([]blsMember, n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		pid := "p" + itoa(i)
		members[i] = mustMintMember(pid, byte(0x80+i))
		ids[i] = pid
	}
	useEligible(t, ids...)
	return members
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestByzantineQuorum_ExactThresholds pins the exact ceil(2n/3) supermajority
// for a spread of committee sizes — including the non-3f+1 sizes (5,6,8,101) the
// old 2f+1 got wrong, and large sizes to confirm it scales.
func TestByzantineQuorum_ExactThresholds(t *testing.T) {
	want := map[int]int{4: 3, 5: 4, 6: 4, 7: 5, 8: 6, 10: 7, 13: 9, 100: 67, 101: 68}
	for n, exp := range want {
		if got := ByzantineQuorum(n); got != exp {
			t.Errorf("ByzantineQuorum(%d) = %d, want %d", n, got, exp)
		}
	}
}

// TestVerifyCertificate_ThresholdPerCommitteeSize proves, for each mandated
// size, that exactly threshold-1 eligible +1 votes is NOT enough and exactly
// threshold IS enough.
func TestVerifyCertificate_ThresholdPerCommitteeSize(t *testing.T) {
	for _, n := range []int{4, 5, 7, 10, 13} {
		n := n
		t.Run("n="+itoa(n), func(t *testing.T) {
			members := committeeOfSize(t, n)
			threshold := ByzantineQuorum(n)
			hash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000" + hex2(n))

			votesFrom := func(k int) map[string]string {
				var v []BLS_Signer.BLSresponse
				for i := 0; i < k; i++ {
					v = append(v, members[i].blockVote(t, hash.Hex(), 1))
				}
				return certData(t, v...)
			}

			// threshold-1 votes → NOT reached.
			if rej := verifyBlockCertificate(blockMsg(hash, votesFrom(threshold-1))); rej == nil || rej.reason != "quorum_not_met" {
				t.Fatalf("n=%d: %d votes must NOT reach 2f+1=%d, got %v", n, threshold-1, threshold, rej)
			}
			// exactly threshold votes → reached.
			if rej := verifyBlockCertificate(blockMsg(hash, votesFrom(threshold))); rej != nil {
				t.Fatalf("n=%d: %d votes must reach 2f+1=%d, got reject %s", n, threshold, threshold, rej.reason)
			}
		})
	}
}

func hex2(n int) string {
	const d = "0123456789abcdef"
	return string([]byte{d[(n>>4)&0xf], d[n&0xf]})
}

// TestSingleVoteCannotFinalize: a single supplied vote must never finalize a
// block, on ANY committee size ≥ 4 (the earlier (validTotal/2)+1 would accept it
// because validTotal=1 → needed=1).
func TestSingleVoteCannotFinalize(t *testing.T) {
	for _, n := range []int{4, 5, 7, 10, 13} {
		members := committeeOfSize(t, n)
		hash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000001" + hex2(n))
		cert := certData(t, members[0].blockVote(t, hash.Hex(), 1))
		if rej := verifyBlockCertificate(blockMsg(hash, cert)); rej == nil || rej.reason != "quorum_not_met" {
			t.Fatalf("n=%d: a single vote must not finalize, got %v", n, rej)
		}
	}
}

// TestSimpleMajorityInsufficient: a simple majority of the committee that is
// below 2f+1 must NOT finalize. For n=7, simple majority = 4 but 2f+1 = 5.
func TestSimpleMajorityInsufficient(t *testing.T) {
	members := committeeOfSize(t, 7)
	hash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000f7")
	var v []BLS_Signer.BLSresponse
	for i := 0; i < 4; i++ { // 4 = simple majority of 7, but < 2f+1 (5)
		v = append(v, members[i].blockVote(t, hash.Hex(), 1))
	}
	if rej := verifyBlockCertificate(blockMsg(hash, certData(t, v...))); rej == nil || rej.reason != "quorum_not_met" {
		t.Fatalf("simple majority (4 of 7) must not reach 2f+1=5, got %v", rej)
	}
}

// TestProcessBlockLocally_SingleVoteRejected exercises the OTHER path that
// used to compute (validTotal/2)+1. It must also refuse a single vote.
func TestProcessBlockLocally_SingleVoteRejected(t *testing.T) {
	members := committeeOfSize(t, 5)
	hash := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000a5")
	single := []BLS_Signer.BLSresponse{members[0].blockVote(t, hash.Hex(), 1)}
	block := &config.ZKBlock{BlockHash: hash, BlockNumber: 42}
	err := ProcessBlockLocally(block, single)
	if err == nil {
		t.Fatal("ProcessBlockLocally must reject a single-vote certificate (2f+1 not met)")
	}
}
