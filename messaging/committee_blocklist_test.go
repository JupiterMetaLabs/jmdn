package messaging

// H3 (2026-10-22 audit): the block_buddy blocklist is node-local, so nothing
// that must agree across the fleet may depend on it. The block committee was
// seated from the blocklist-filtered pool, so a node blocking one peer of a
// 7-member committee computed n=6 and a threshold of 4 where every peer
// computed n=7 and 5, and accepted blocks its peers rejected. These tests pin
// the rule VerifyCertificate (CON-12) and the entropy fold (D-36) already
// follow: the seats and the threshold come from the fleet pool; the blocklist
// only stops this node counting a vote.

import (
	"math/big"
	"sort"
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

func blocklistTestBlock(height uint64) *config.ZKBlock {
	return &config.ZKBlock{
		BlockNumber:   height,
		BlockHash:     common.BigToHash(new(big.Int).SetUint64(height)),
		ConsensusHash: common.HexToHash(testBlockConsensusHash),
	}
}

func seatedIDs(t *testing.T, rc RoundContext) []string {
	t.Helper()
	seated, err := SelectCommittee(rc)
	if err != nil {
		t.Fatalf("SelectCommittee: %v", err)
	}
	ids := make([]string, 0, len(seated))
	for _, m := range seated {
		ids = append(ids, m.PeerID)
	}
	sort.Strings(ids)
	return ids
}

// A local blocklist entry must not change who is seated, whether the pool is
// the committee size (7 of 7) or larger (7 of 10, where the seed-ranked draw
// over a different pool would seat a different committee).
func TestSelectCommittee_IgnoresLocalBlocklist(t *testing.T) {
	for _, pool := range []int{7, 10} {
		enableV2(t)
		poolOf(t, pool, 7)
		rc := RoundContextForBlock(blocklistTestBlock(3000))

		before := seatedIDs(t, rc)
		withBlockBuddy(t, before[0])
		after := seatedIDs(t, rc)

		if len(after) != len(before) {
			t.Fatalf("pool %d: blocking %s changed the committee size from %d to %d", pool, before[0], len(before), len(after))
		}
		for i := range before {
			if before[i] != after[i] {
				t.Fatalf("pool %d: blocking %s changed the committee: %v -> %v", pool, before[0], before, after)
			}
		}
	}
}

// The audit's example: 7 seats, one peer blocked here, 4 valid votes from the
// other seats. Every node needs 5, so no node may call this block final.
func TestVerifyCertificateForRound_BlocklistCannotLowerThreshold(t *testing.T) {
	enableV2(t)
	members := poolOf(t, 7, 7)
	block := blocklistTestBlock(3001)
	hash := block.BlockHash.Hex()
	rc := RoundContextForBlock(block)

	var votes []BLS_Signer.BLSresponse
	for _, m := range members[:4] {
		votes = append(votes, m.blockVoteAt(t, hash, 1, block.BlockNumber))
	}
	withBlockBuddy(t, members[6].peerID)

	got, err := VerifyCertificateForRound(votes, hash, testBlockConsensusHash, block.BlockNumber, rc)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitteeSize != 7 || got.Threshold != 5 {
		t.Fatalf("a local blocklist must not move n or the threshold: got n=%d threshold=%d, want 7 and 5", got.CommitteeSize, got.Threshold)
	}
	if got.Reached {
		t.Fatalf("4 votes of 7 must not be final on the blocklisting node when every peer needs 5: %+v", got)
	}
}

// Blocking still works: the blocked peer's vote is not counted, so blocking
// can only make quorum harder.
func TestVerifyCertificateForRound_BlockedVoteNotCounted(t *testing.T) {
	enableV2(t)
	members := poolOf(t, 7, 7)
	block := blocklistTestBlock(3002)
	hash := block.BlockHash.Hex()
	rc := RoundContextForBlock(block)

	var votes []BLS_Signer.BLSresponse
	for _, m := range members[:5] {
		votes = append(votes, m.blockVoteAt(t, hash, 1, block.BlockNumber))
	}

	open, err := VerifyCertificateForRound(votes, hash, testBlockConsensusHash, block.BlockNumber, rc)
	if err != nil || !open.Reached || open.YesVotes != 5 {
		t.Fatalf("precondition: 5 of 7 must be final with no blocklist: (%+v, %v)", open, err)
	}

	withBlockBuddy(t, members[4].peerID)
	got, err := VerifyCertificateForRound(votes, hash, testBlockConsensusHash, block.BlockNumber, rc)
	if err != nil {
		t.Fatal(err)
	}
	if got.YesVotes != 4 || got.Reached {
		t.Fatalf("a blocked peer's vote must not count: got yes=%d reached=%v, want 4 and false", got.YesVotes, got.Reached)
	}
	if got.CommitteeSize != 7 || got.Threshold != 5 {
		t.Fatalf("the blocked seat must still size n: got n=%d threshold=%d", got.CommitteeSize, got.Threshold)
	}
}

// The sync path's pool-wide tally sizes from the fleet pool and, like every
// other tally, does not count a blocked peer's vote.
func TestPoolWideSyncTally_BlockedVoteNotCounted(t *testing.T) {
	enableV2(t)
	members := poolOf(t, 10, 7)
	block := blocklistTestBlock(3003)
	hash := block.BlockHash.Hex()

	var votes []BLS_Signer.BLSresponse
	for _, m := range members[5:] {
		votes = append(votes, m.blockVoteAt(t, hash, 1, block.BlockNumber))
	}
	withBlockBuddy(t, members[9].peerID)

	got, err := verifySyncedCertificatePoolWide(votes, hash, testBlockConsensusHash, block.BlockNumber)
	if err != nil {
		t.Fatal(err)
	}
	if got.YesVotes != 4 || got.Reached {
		t.Fatalf("a blocked peer's vote must not count on the pool-wide tally: got yes=%d reached=%v", got.YesVotes, got.Reached)
	}
	if got.PoolSize != 10 || got.CommitteeSize != 7 || got.Threshold != 5 {
		t.Fatalf("the pool-wide threshold must not move: got pool=%d n=%d threshold=%d", got.PoolSize, got.CommitteeSize, got.Threshold)
	}
}

// The sequencer sizes its round-formation quorum from FleetCommitteeSize; it
// must ignore the local blocklist the way the eligible set does not.
func TestFleetCommitteeSize_IgnoresLocalBlocklist(t *testing.T) {
	members := poolOf(t, 7, 7)
	withBlockBuddy(t, members[0].peerID)

	n, err := FleetCommitteeSize()
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("FleetCommitteeSize = %d with one peer blocked, want 7", n)
	}
	local, err := EligibleCommitteePeerIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 6 {
		t.Fatalf("precondition: the local eligible set must still drop the blocked peer, got %d", len(local))
	}
}
