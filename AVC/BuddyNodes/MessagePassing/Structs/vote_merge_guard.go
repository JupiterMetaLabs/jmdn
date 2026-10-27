package Structs

import (
	"fmt"
	"strings"

	"gossipnode/config"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

// A-3 pre-fill and H7 (2026-10-22 audit). A buddy's CRDT sync merge writes
// remote elements straight into the vote CRDT, so whatever it stores counts
// toward avc's per-peer cap (CountElementsForPeer counts every element with
// the "<peer>:" prefix). Before this guard:
//
//   - a votes: element was stored with no check, so "<victim>:x" junk filled
//     the victim's cap and avc's AddVote then refused the victim's own vote;
//   - an element with no colon had no prefix and skipped the cap, and every
//     invented peer ID got a cap of its own, so a key grew without bound;
//   - the cap was recounted over the whole set for every element (O(n²)).
//
// VoteMergeGuard decides element by element what a merge may store for one
// votes: or votesig: key. It reads the stored set once and keeps its own
// counts, so a merge is linear in the elements it sees.
//
// # The rules
//
// Every key: the element must be well formed, and its voter must be in the
// height's voter set when that set is known.
//
// votes: elements are exactly "<peer>:1" or "<peer>:-1". Other spellings
// that parse to the same number ("01", "+1") are distinct elements and are
// refused. A peer can therefore hold at most two, below the cap of three, so
// no merge can fill the cap AddVote checks.
//
// votesig: records that verify (SigMergeVerifier) are stored at most once
// per (peer, vote). A signature does not cover RejectionReason or JSON
// spelling, so one genuine record has many verifying variants; keeping one
// stops them crowding out the same peer's other vote, which is equivocation
// evidence.
//
// votesig: records this node cannot verify yet (no local block, or no voter
// set) are kept, so a genuine record is not lost, but bounded per peer and
// per key. They do not count against verified records: once the block is
// known, a genuine record is stored however much junk came first.
type VoteMergeGuard struct {
	height    uint64
	blockHash string
	sig       bool

	// authorized is the height's voter set, nil when it is unavailable.
	authorized map[string]string
	// verifier checks votesig: records; nil on votes: keys and when this
	// node cannot verify yet.
	verifier *SigMergeVerifier
	// limit bounds the elements a key may hold that were never verified.
	limit int

	perPeer map[string]int // stored well-formed elements per voter
	total   int            // stored well-formed elements

	// votesig: only. stored holds this key's stored records by voter, so a
	// voter's verified (peer, vote) pairs can be found when it first needs
	// them; verified caches the result per voter.
	stored   map[string][]string
	verified map[string]map[int8]bool
}

// NewVoteMergeGuard builds the guard for key from the elements this node
// already stores under it. It fails only for a key that is not a block vote
// key.
func NewVoteMergeGuard(key string, storedElems []string) (*VoteMergeGuard, error) {
	g := &VoteMergeGuard{perPeer: make(map[string]int)}
	var ok bool
	if g.height, g.blockHash, ok = splitSigKey(key); ok {
		g.sig = true
	} else if g.height, g.blockHash, ok = splitVoteKey(key); !ok {
		return nil, fmt.Errorf("not a block vote key: %q", key)
	}

	if authorized, err := authorizedVotersFor(g.height); err == nil && len(authorized) > 0 {
		g.authorized = authorized
		g.limit = len(authorized) * avcvotes.MaxElementsPerPeerPerBlock
	} else {
		g.limit = config.MaxMainPeers * avcvotes.MaxElementsPerPeerPerBlock
	}
	if g.sig {
		if v, err := NewSigMergeVerifier(key); err == nil {
			g.verifier = v
		}
		g.stored = make(map[string][]string)
		g.verified = make(map[string]map[int8]bool)
	}

	for _, e := range storedElems {
		if peerID, _, ok := g.parse(e); ok {
			if g.authorized != nil {
				if _, member := g.authorized[peerID]; !member {
					// Stored while the voter set was unknown. Counting it
					// would let fakes fill the bound and lock a real voter
					// out for good: a votes: element is never verifiable.
					continue
				}
			}
			g.perPeer[peerID]++
			g.total++
			if g.sig {
				g.stored[peerID] = append(g.stored[peerID], e)
			}
		}
	}
	return g, nil
}

// Admit reports whether elem may be stored, and records it as stored if so.
// The caller must not offer an element that is already stored.
func (g *VoteMergeGuard) Admit(elem string) bool {
	peerID, vote, ok := g.parse(elem)
	if !ok {
		return false
	}
	if g.authorized != nil {
		if _, member := g.authorized[peerID]; !member {
			return false
		}
	}

	if g.verifier != nil {
		if !g.verifier.Verify(elem) {
			return false
		}
		have := g.verifiedFor(peerID)
		if have[vote] {
			return false // this (peer, vote) already has a verified record
		}
		have[vote] = true
	} else {
		if g.perPeer[peerID] >= avcvotes.MaxElementsPerPeerPerBlock || g.total >= g.limit {
			return false
		}
	}

	g.perPeer[peerID]++
	g.total++
	if g.sig {
		g.stored[peerID] = append(g.stored[peerID], elem)
	}
	return true
}

// verifiedFor returns the votes peerID already has a verified stored record
// for, verifying its stored records the first time it is asked.
func (g *VoteMergeGuard) verifiedFor(peerID string) map[int8]bool {
	if have, ok := g.verified[peerID]; ok {
		return have
	}
	have := make(map[int8]bool, 2)
	for _, e := range g.stored[peerID] {
		if _, rec, ok := parseSigElement(e); ok && g.verifier.Verify(e) {
			have[rec.Vote] = true
		}
	}
	g.verified[peerID] = have
	return have
}

// parse returns elem's voter and vote if it is a well-formed element for this
// guard's key.
func (g *VoteMergeGuard) parse(elem string) (string, int8, bool) {
	if g.sig {
		peerID, rec, ok := parseSigElement(elem)
		if !ok || peerID == "" || rec.Height != g.height || rec.BlockHash != g.blockHash || (rec.Vote != 1 && rec.Vote != -1) {
			return "", 0, false
		}
		return peerID, rec.Vote, true
	}
	peerID, voteStr, found := strings.Cut(elem, ":")
	if !found || peerID == "" {
		return "", 0, false
	}
	switch voteStr {
	case "1":
		return peerID, 1, true
	case "-1":
		return peerID, -1, true
	}
	return "", 0, false
}

// splitVoteKey splits avc's "votes:<height>:<blockHash>" into its height and
// hash.
func splitVoteKey(key string) (uint64, string, bool) {
	rest, ok := strings.CutPrefix(key, "votes:")
	if !ok {
		return 0, "", false
	}
	return splitHeightHash(rest)
}
