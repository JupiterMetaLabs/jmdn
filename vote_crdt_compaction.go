package main

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"gossipnode/Vote"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/internal/reputation"
	"gossipnode/messaging"
	"gossipnode/metrics"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

// Stage 6 (docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md §8): drive the vote CRDT's
// ConvergeAndCompact from the same monotonic choke point that already drives
// event-driven seed reporting (seed_blockhead_push.go) — DB_OPs'
// UpdateLatestBlockMonotonic onAdvance hook. Same non-blocking, debounced,
// coalescing shape as newSeedBlockHeadPusher for the same reason: onAdvance
// fires under DB_OPs' latestBlockMu and its contract forbids blocking or
// calling back into DB_OPs, and ConvergeAndCompact (a CRDT scan plus one
// TallyBlock per newly-converging block) is real work that must never run
// inline there.

// voteCRDTCompactionDebounce mirrors seedBlockHeadPushDebounce: coalesce a
// burst of applied blocks (catch-up) into one compaction pass over the
// final tip, since ConvergeAndCompact re-derives everything from the tip it
// is given and re-running it once per intermediate block buys nothing.
const voteCRDTCompactionDebounce = 750 * time.Millisecond

// voteCRDTCompactK is the compaction safety buffer (LLD §9 Decision 2):
// votes for heights within K of the tip are retained; only what falls at or
// below tip-K is evaluated and compacted. Assumed 128, never measured — see
// the LLD for the reasoning. Overridable for operators who do measure their
// own vote-arrival/catch-up lag, via JMDN_VOTE_CRDT_COMPACT_K.
var voteCRDTCompactK = envUint64("JMDN_VOTE_CRDT_COMPACT_K", 128)

func envUint64(key string, def uint64) uint64 {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// equivocationReputationReporter implements avc/crdt/votes.EquivocationReporter
// over jmdn's existing reputation.Equivocation event (LLD §8.3) — not a new
// category. blockHash/height/values are the evidence backing the verdict;
// reputation.Observe's current signature only takes peerID and Event, so
// they are logged here rather than dropped silently, in case a future
// Observe signature (or an operator reading logs) wants them.
//
// A4-COMPLETION-LLD.md §2: wired live (was nil through Stage 6's initial
// rollout, per the LLD's own "land with nil first, confirm it deletes
// correctly" recommendation — that confirmation has now happened).
type equivocationReputationReporter struct{}

func (equivocationReputationReporter) ReportEquivocation(peerID, blockHash string, height uint64, values []int8) {
	log.Warn().
		Str("peer_id", peerID).
		Str("block_hash", blockHash).
		Uint64("height", height).
		Interface("values", values).
		Msg("[VoteCRDTCompaction] equivocation confirmed at convergence — reporting")

	// A4-COMPLETION-LLD.md §3.3 (Design A): visibility, not a fix. This
	// counter increments independently on every node that observes this
	// fault against its own local CRDT copy — comparing it across nodes for
	// the same peer_id is how the §3.1 cross-node convergence gap would
	// ever actually be noticed, rather than silently assumed closed by the
	// K-buffer.
	metrics.ReputationEquivocationsReportedCounter.WithLabelValues(peerID).Inc()

	if !reputation.Enabled {
		return
	}
	reputation.Default.Observe(peerID, reputation.Equivocation)

	// A4-COMPLETION-LLD.md §5: equivocation is the one event severe enough
	// (straight to Floor) to justify pushing sooner than the routine
	// interval — everything else stays on the 5-minute tick.
	// Non-blocking, never fires reputation.Enabled == false above; also
	// safe (a harmless no-op) on a node whose pusher hasn't started yet, or
	// isn't the sequencer — triggerImmediateReputationPush only ever
	// signals a wake, pushReputationOnce itself still checks
	// PushReputationWeights' own ErrNotSequencer gate.
	triggerImmediateReputationPush()
}

// startVoteCRDTCompactionHook launches the background compactor with the
// production debounce window and K, and returns the fire-and-forget hook to
// chain into DB_OPs.SetLatestBlockAdvanceHook alongside the seed pusher.
func startVoteCRDTCompactionHook(ctx context.Context) func(uint64) {
	return newVoteCRDTCompactionHook(ctx, compactConvergedVotes, voteCRDTCompactionDebounce, voteCRDTCompactK)
}

// newVoteCRDTCompactionHook is the testable core, same shape as
// newSeedBlockHeadPusher: the returned hook only records the latest tip and
// signals a worker goroutine — it never blocks and never calls trigger
// inline, so it is safe to run under DB_OPs' latestBlockMu. A nil trigger
// yields a no-op hook.
func newVoteCRDTCompactionHook(ctx context.Context, trigger func(context.Context, uint64, uint64), debounce time.Duration, k uint64) func(uint64) {
	if trigger == nil {
		return func(uint64) {}
	}

	var pending atomic.Bool
	var lastTip atomic.Uint64
	wake := make(chan struct{}, 1)

	hook := func(tip uint64) {
		lastTip.Store(tip)
		pending.Store(true)
		select {
		case wake <- struct{}{}:
		default:
		}
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(debounce):
			}
			if pending.Swap(false) {
				trigger(ctx, lastTip.Load(), k)
			}
		}
	}()

	return hook
}

// compactConvergedVotes is the production trigger: resolve the live
// listener node and committee, then run ConvergeAndCompact. Every failure
// mode here skips this round's compaction and retries on the next advance —
// none of them are fatal, since a delayed compaction only costs memory, and
// jmdn's own consensus state is untouched either way.
func compactConvergedVotes(ctx context.Context, tip, k uint64) {
	if !Vote.VoteCRDTDualWrite {
		return // v2 CRDT unused — nothing to compact
	}

	listenerNode := AVCStruct.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil || listenerNode.VoteCRDTLayer == nil {
		return
	}

	authorized, err := messaging.AuthorizedCommittee()
	if err != nil {
		log.Warn().Err(err).Msg("[VoteCRDTCompaction] authorized committee unavailable — skipping this round")
		return
	}

	// A4-COMPLETION-LLD.md §2: reporter is live (was nil through Stage 6's
	// initial rollout — see equivocationReputationReporter's doc comment
	// for the confirmation this follow-up step refers to).
	evaluated, deleted, err := avcvotes.DefaultWatermark.ConvergeAndCompact(
		listenerNode.VoteCRDTLayer, tip, k, authorized, equivocationReputationReporter{})
	if err != nil {
		log.Warn().Err(err).Uint64("tip", tip).Uint64("k", k).Msg("[VoteCRDTCompaction] ConvergeAndCompact failed")
		return
	}
	if evaluated > 0 || deleted > 0 {
		log.Info().
			Uint64("tip", tip).
			Uint64("k", k).
			Int("evaluated", evaluated).
			Int("deleted", deleted).
			Msg("[VoteCRDTCompaction] converged and compacted")
	}
}
