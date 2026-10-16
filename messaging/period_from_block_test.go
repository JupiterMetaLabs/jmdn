package messaging

import (
	"fmt"
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// The corner case the period-from-block design exists for: a node that was
// offline (or restarted) while height 885 failed a round comes back and syncs
// the block that finally committed at Period 1. It holds no period state of
// any kind - there is none - and must verify the block from the block alone.
//
// It must also seat the committee of the BLOCK's period: votes from the
// Period-0 committee do not certify a Period-1 block.
func TestVerifySyncedBlockCertificate_PeriodComesFromTheBlock(t *testing.T) {
	enableV2(t)
	env := withAnchoring(t, 860)
	// Pool larger than the seat count, so a new period seats a new committee.
	snap, members := mintedSnapshot(t, env.auth, 500, 29)
	if err := RecordCommitteeAnchor(anchorBlockFor(t, 860, 900, snap)); err != nil {
		t.Fatal(err)
	}
	env.setTip(899)

	byID := make(map[string]blsMember, len(members))
	for _, m := range members {
		byID[m.peerID] = m
	}
	votesFrom := func(b *config.ZKBlock) []BLS_Signer.BLSresponse {
		t.Helper()
		seated, err := SelectCommittee(RoundContextForBlock(b))
		if err != nil {
			t.Fatalf("SelectCommittee: %v", err)
		}
		var votes []BLS_Signer.BLSresponse
		for _, m := range seated {
			votes = append(votes, byID[m.PeerID].blockVoteAt(t, b.BlockHash.Hex(), 1, b.BlockNumber))
		}
		return votes
	}

	// Find a parent hash where the Period-0 committee's votes fall short for
	// the Period-1 block, so the test proves the period picks the committee.
	for i := range 64 {
		prev := common.HexToHash(fmt.Sprintf("0x%064x", i+1))
		atP1 := &config.ZKBlock{BlockNumber: 885, Slot: 885, Period: 1, PrevHash: prev}
		atP0 := &config.ZKBlock{BlockNumber: 885, Slot: 885, Period: 0, PrevHash: prev}

		if rc := RoundContextForBlock(atP1); rc.Period != 1 {
			t.Fatalf("round context must carry the block's own period, got %d", rc.Period)
		}

		res, err := VerifySyncedBlockCertificate(atP1, votesFrom(atP1))
		if err != nil {
			t.Fatalf("a node with no period state must verify a Period-1 block: %v", err)
		}
		if !res.Reached {
			t.Fatalf("the Period-1 committee's certificate must reach quorum: %+v", res)
		}

		wrong, err := VerifySyncedBlockCertificate(atP1, votesFrom(atP0))
		if err != nil {
			t.Fatalf("VerifySyncedBlockCertificate: %v", err)
		}
		if !wrong.Reached {
			return // Period-0 votes do not certify the Period-1 block.
		}
	}
	t.Fatal("in 64 rounds the Period-0 committee always certified the Period-1 block: the period is not selecting the committee")
}
