package Sequencer

// Reputation attribution under committee v2 (fix 2 of the seat/candidate
// mismatch).
//
// THE DEFECT
//
// ProcessVoteCollection classifies every peer in consensus.PeerList.MainPeers
// for reputation, and a MainPeer with no collected response is charged
// Absent (-0.10). That was correct while MainPeers WAS the committee.
//
// Under JMDN_COMMITTEE_V2 it is not. MainPeers is "the first MaxMainPeers
// reachable candidates": the seated peers first, then UNSEATED candidates
// filling any remaining slots. SetZKBlockData puts only the SEATED ones on the
// block's buddy list, so a filler receives the vote trigger, sees it is not in
// the buddy list, logs "I'm not in the buddy list for this round - skipping"
// (AVC/BuddyNodes/MessagePassing/ListenerHandler.go) and never votes. It is
// then charged Absent for a vote it was never asked for.
//
// Those scores are pushed to the seed as selection weights every 5 minutes
// (reputation_seed_push.go). Three such charges push a fresh peer's weight
// below the NodeSelection band, dropping it from the candidate pool - which
// created more missing seats, more fillers and more false Absents: the
// feedback loop that shrank the pool to 7 of 29.
//
// THE FIX
//
// Classify only the peers that were actually asked: MainPeers that are ALSO
// on the block's buddy list (the wire list the validators and buddies act
// on). A peer outside that list was never told it was a buddy, so its silence
// is not a fault and it is neither charged nor rewarded.
//
// WHAT THIS DOES NOT CHANGE
//
//   - The tally, the committee draw, or who is asked to vote.
//   - Genuine faults: a seated, on-the-wire peer that does not answer is still
//     Absent; bad signatures and equivocation are charged at their own sites.
//   - A seated peer that could not be reached at all is not on the wire list
//     and was already not in MainPeers, so it was already not charged; that is
//     unchanged (the sequencer cannot tell a peer fault from its own
//     connectivity problem).
//   - Behaviour with JMDN_COMMITTEE_V2 off: the caller keeps classifying
//     MainPeers exactly as before.

import (
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ReputationCommittee returns, in MainPeers order and without duplicates, the
// peers a round's reputation may classify: those in mainPeers that are also on
// the block's buddy list.
//
// The buddy list is read the way a receiving node reads it
// (messaging.handleVoteTriggerBroadcast): keys 0..config.MaxMainPeers-1 of the
// consensus message's Buddies map. A peer the receivers would not treat as a
// buddy is not treated as one here either.
//
// An empty or nil buddy list yields an empty committee: nobody was asked, so
// nobody is charged.
func ReputationCommittee(
	mainPeers []peer.ID,
	buddies map[int]PubSubMessages.Buddy_PeerMultiaddr,
) []string {
	onWire := make(map[peer.ID]struct{}, len(buddies))
	for i := 0; i < config.MaxMainPeers && i < len(buddies); i++ {
		if b, ok := buddies[i]; ok && b.PeerID != "" {
			onWire[b.PeerID] = struct{}{}
		}
	}

	out := make([]string, 0, len(mainPeers))
	seen := make(map[peer.ID]struct{}, len(mainPeers))
	for _, p := range mainPeers {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		if _, ok := onWire[p]; ok {
			out = append(out, p.String())
		}
	}
	return out
}
