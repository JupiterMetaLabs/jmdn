package Sequencer

// Seat-ordering of buddy candidates.
//
// WHY THIS FILE EXISTS
//
// Under JMDN_COMMITTEE_V2 the sequencer's candidate pool is deliberately
// UNCAPPED: Consensus.Start filters warmup candidates against
// messaging.WarmupPeerIDs(), which is the whole eligible pool, not the capped
// prefix that used to be seated. The seated committee then rotates across that
// pool every height (messaging.SelectCommittee).
//
// Everything downstream of that filter, however, is order-sensitive and
// TRUNCATING:
//
//   - Consensus.ConnectedNessCheck stops at maxPeers, keeping whichever
//     candidates it happened to walk first.
//   - The Step-3 split in Consensus.Start takes the first config.MaxMainPeers
//     connected candidates as MainPeers and the rest as backup.
//
// The slice fed to the first of those was built by helper.ConvertMapToSlice,
// which ranges over a Go map — i.e. in randomized order. So with a pool larger
// than MaxMainPeers+MaxBackupPeers the sequencer could dial, seat and ask for
// votes from peers that are NOT on the committee seated for this round, while
// omitting peers that are. Those votes are then rejected as unauthorized at
// tally time and the round loses quorum for no visible reason: the failure
// surfaces as a missing-quorum halt, not as a selection error.
//
// The fix is ordering, not filtering. OrderCandidatesBySeat moves the peers
// seated for THIS round to the front, in seat order, and leaves every other
// candidate behind them in its original relative order.
// ReachableInCandidateOrder then preserves that order across the
// map-shaped reachability result, so the truncation that follows cuts the tail
// (unseated backups) instead of a random slice of the committee.
//
// Both functions are total and non-lossy: no candidate is dropped and none is
// duplicated, so turning seat-ordering off changes nothing but the order.

import (
	"sort"

	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// OrderCandidatesBySeat returns candidates reordered so that the members of
// seated come first, in seat order, followed by every remaining candidate in
// its original relative order.
//
// It returns, as missingSeats, the string peer id of every seat that could not
// be matched to a candidate — either because the peer is not in the candidate
// pool (not warmed up, or unreachable) or because its peer id does not parse.
// A non-empty missingSeats is a real signal: the sequencer cannot ask those
// seats to vote this round, so quorum has to come from the seats it did match.
// The caller decides what to do about it; this function never fails.
//
// Invariants (asserted by the tests):
//   - len(ordered) == len(candidates) — nothing is added or dropped.
//   - ordered is a permutation of candidates.
//   - seated == nil (or empty) means the order is unchanged.
//   - A seat is matched at most once, so a duplicated candidate peer id keeps
//     its extra copies in the tail rather than consuming two seats.
func OrderCandidatesBySeat(
	candidates []PubSubMessages.Buddy_PeerMultiaddr,
	seated []committee.Member,
) (ordered []PubSubMessages.Buddy_PeerMultiaddr, missingSeats []string) {
	if len(candidates) == 0 {
		// Every seat is unmatched, but there is nothing to reorder.
		for _, m := range seated {
			missingSeats = append(missingSeats, m.PeerID)
		}
		return candidates, missingSeats
	}
	if len(seated) == 0 {
		return candidates, nil
	}

	// Candidate indices per peer id, so a duplicate peer id can only be
	// consumed once per seat.
	byPeer := make(map[peer.ID][]int, len(candidates))
	for i, c := range candidates {
		byPeer[c.PeerID] = append(byPeer[c.PeerID], i)
	}

	taken := make([]bool, len(candidates))
	ordered = make([]PubSubMessages.Buddy_PeerMultiaddr, 0, len(candidates))

	for _, m := range seated {
		pid, err := peer.Decode(m.PeerID)
		if err != nil {
			// An unparseable seat is a missing seat, never a panic and never a
			// silently skipped one.
			missingSeats = append(missingSeats, m.PeerID)
			continue
		}
		placed := false
		for _, i := range byPeer[pid] {
			if taken[i] {
				continue
			}
			taken[i] = true
			ordered = append(ordered, candidates[i])
			placed = true
			break
		}
		if !placed {
			missingSeats = append(missingSeats, m.PeerID)
		}
	}

	// Unseated candidates keep their original relative order.
	for i := range candidates {
		if !taken[i] {
			ordered = append(ordered, candidates[i])
		}
	}

	return ordered, missingSeats
}

// ReachableInCandidateOrder projects a map-shaped reachability result back onto
// the candidate order.
//
// helper.ConvertMapToSlice ranges over the map, so its output order is
// randomized per call; feeding that to a function that truncates at maxPeers
// makes the surviving set nondeterministic. This returns the same peers in
// candidate order — which, after OrderCandidatesBySeat, is seat order — so a
// truncation drops the tail rather than an arbitrary subset.
//
// The multiaddr comes from the reachable map, not from the candidate: that is
// the address the peer was actually reached on.
//
// Reachable peers that are not in candidates cannot normally occur (the map is
// derived from the same candidate list) but are appended, sorted by peer id, so
// the result is total and deterministic either way.
func ReachableInCandidateOrder(
	candidates []PubSubMessages.Buddy_PeerMultiaddr,
	reachable map[peer.ID]multiaddr.Multiaddr,
) []PubSubMessages.Buddy_PeerMultiaddr {
	out := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, len(reachable))
	if len(reachable) == 0 {
		return out
	}

	emitted := make(map[peer.ID]struct{}, len(reachable))
	for _, c := range candidates {
		addr, ok := reachable[c.PeerID]
		if !ok {
			continue
		}
		if _, dup := emitted[c.PeerID]; dup {
			continue
		}
		emitted[c.PeerID] = struct{}{}
		out = append(out, PubSubMessages.Buddy_PeerMultiaddr{
			PeerID:    c.PeerID,
			Multiaddr: addr,
		})
	}

	if len(out) == len(reachable) {
		return out
	}

	rest := make([]peer.ID, 0, len(reachable)-len(out))
	for pid := range reachable {
		if _, ok := emitted[pid]; !ok {
			rest = append(rest, pid)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
	for _, pid := range rest {
		out = append(out, PubSubMessages.Buddy_PeerMultiaddr{
			PeerID:    pid,
			Multiaddr: reachable[pid],
		})
	}
	return out
}
