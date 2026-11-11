package Structs

// Bounds on what a received validator vote may write into the vote CRDT.
//
// IngestValidatorVote used to rely on avcvotes.AddVote's per-peer cap
// (MaxElementsPerPeerPerBlock = 3) to bound spam (LLD D5). That cap counts
// the peer's elements under the VOTE key, which only ever holds "peer:1" and
// "peer:-1", so it never fires; the SIG key, which holds the ~1 KB payloads,
// had no cap. With no voter-set check and no height window, any connected
// peer could write unbounded data under unbounded (height, block) keys, and
// heights above the compaction watermark are never swept.
//
// Each received vote must now pass, in order:
//
//  1. height window: at most MaxVoteHeightAhead above this node's committed
//     tip (below the watermark AddVote already refuses it);
//  2. voter set: the sender is in the fleet-agreed voter pool
//     (ingestVotersForHeightFn: the tally's pool without the node-local
//     block_buddy filter). A stored vote is republished by CRDT sync, so a
//     node must store what any peer would count, not only what it counts
//     itself; the tally still applies the filtered set. When the source is
//     unset or cannot answer, ingest stays lossless (LLD D5) and only the
//     per-peer bounds below apply;
//  3. per-peer cap on the SIG key (the cap AddVote meant to apply);
//  4. at most MaxBlocksPerVoterPerHeight distinct block hashes per
//     (sender, height), so one voter cannot open unbounded keys at a height.
//
// Together: memory is bounded by voters x window x blocks x 3 x element size.
// JMDN_VOTE_INGEST_LIMITS=0 turns all four off (the previous behaviour).

import (
	"errors"
	"strconv"
	"sync"
	"time"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	avctypes "github.com/JupiterMetaLabs/avc/types"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/libp2p/go-libp2p/core/peer"
)

// VoteIngestLimitsEnabled is the rollback switch for these bounds.
var VoteIngestLimitsEnabled = envOnStructs("JMDN_VOTE_INGEST_LIMITS", true)

// MaxVoteHeightAhead is how far past the committed tip a received vote's
// height may be. Live votes are for tip+1; a node further behind than this is
// catching up and cannot tally those heights anyway. A variable for tests.
var MaxVoteHeightAhead uint64 = 32

// MaxBlocksPerVoterPerHeight bounds the distinct block hashes one voter may
// write votes for at one height. An honest voter votes once per round, and a
// height only sees several rounds when proposals keep failing.
const MaxBlocksPerVoterPerHeight = 8

// ErrVoteIngestRefused wraps every refusal from these bounds.
var ErrVoteIngestRefused = errors.New("validator vote ingest: refused")

// committedTipFn returns this node's highest committed height and whether any
// block has been committed yet. Injected (messaging.CommittedTip) for the
// same import-cycle reason as authorizedCommitteeFn in committee_source.go.
var committedTipFn func() (uint64, bool)

// SetCommittedTipFn wires the committed-tip source. Call once at startup.
func SetCommittedTipFn(fn func() (uint64, bool)) {
	committedTipFn = fn
}

// ingestVotersForHeightFn returns the voters whose votes a node may store at a
// height: the fleet-agreed pool (messaging.FleetVotersForIngestAtHeight), not
// the blocklist-filtered set the tally counts from (authorizedVotersFor).
// Injected for the same import-cycle reason as committedTipFn.
var ingestVotersForHeightFn func(height uint64) (map[string]string, error)

// SetIngestVotersForHeightFn wires the ingest voter-set source. Call once at
// startup.
func SetIngestVotersForHeightFn(fn func(height uint64) (map[string]string, error)) {
	ingestVotersForHeightFn = fn
}

// voterBlocks records, per "sender|height", the block hashes that sender has
// had a vote stored for. Size-bounded and expiring, so it cannot itself grow
// without limit; an entry outlives its height's place in the window.
var (
	voterBlocksMu sync.Mutex
	voterBlocks   = expirable.NewLRU[string, []string](16384, nil, time.Hour)
)

func voterBlocksKey(sender peer.ID, height uint64) string {
	return sender.String() + "|" + strconv.FormatUint(height, 10)
}

// checkVoteIngestLimits returns "" when the vote may be written, or the
// refusal reason (also the metrics label).
func checkVoteIngestLimits(layer *avctypes.Controller, sender peer.ID, height uint64, blockHash string) string {
	if !VoteIngestLimitsEnabled {
		return ""
	}
	if fn := committedTipFn; fn != nil {
		if tip, ok := fn(); ok && height > tip+MaxVoteHeightAhead {
			return "height_ahead"
		}
	}
	if fn := ingestVotersForHeightFn; fn != nil {
		if voters, err := fn(height); err == nil {
			if _, ok := voters[sender.String()]; !ok {
				return "not_voter"
			}
		}
	}
	if avcvotes.CountElementsForPeer(layer, avcvotes.BlockSigKey(height, blockHash), sender.String()) >= avcvotes.MaxElementsPerPeerPerBlock {
		return "peer_cap"
	}
	voterBlocksMu.Lock()
	defer voterBlocksMu.Unlock()
	if hashes, ok := voterBlocks.Get(voterBlocksKey(sender, height)); ok {
		for _, h := range hashes {
			if h == blockHash {
				return ""
			}
		}
		if len(hashes) >= MaxBlocksPerVoterPerHeight {
			return "block_cap"
		}
	}
	return ""
}

// noteVoteIngested records a stored vote's block hash for its sender and height.
func noteVoteIngested(sender peer.ID, height uint64, blockHash string) {
	if !VoteIngestLimitsEnabled {
		return
	}
	key := voterBlocksKey(sender, height)
	voterBlocksMu.Lock()
	defer voterBlocksMu.Unlock()
	hashes, _ := voterBlocks.Get(key)
	for _, h := range hashes {
		if h == blockHash {
			return
		}
	}
	voterBlocks.Add(key, append(hashes, blockHash))
}
