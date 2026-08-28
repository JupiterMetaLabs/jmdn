package main

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"gossipnode/internal/reputation"
	"gossipnode/metrics"
	"gossipnode/seednode"
)

// reputationPusher is the minimal seam pushReputationOnce needs — satisfied
// today by *seednode.Client without any change to that package (its
// PushReputationWeights method already has exactly this shape). Exists so
// reputation_seed_push_test.go can inject a fake without touching
// seednode's unexported Client.client field or standing up a real gRPC
// connection, per A4-COMPLETION-LLD.md §6's note that this seam was
// missing.
type reputationPusher interface {
	PushReputationWeights(ctx context.Context, weights map[string]float64) (accepted int, failures []error)
}

// Phase A4.2 (docs/A4-REPUTATION-WEIGHTING-PLAN.md): periodically push this
// node's observed reputation scores (internal/reputation, observe-only) to
// the seed as peer.Weights, remapped through reputation.SelectionWeight
// (Decision A4-1) so they land sanely against AVC/NodeSelection/pkg/selection
// /filter.go's eligibility band. See seednode/sequencer_reputation_push.go's
// PHASE A4.2 CAVEAT comment for exactly what this does and does not
// guarantee today (functionally works; not yet cryptographically enforced by
// the seed).
//
// Interval, not event-driven: unlike the vote-CRDT compaction hook or the
// seed block-head pusher (both fire off a monotonic block-advance signal),
// reputation accumulates gradually over many rounds (Delta is +-0.02..0.50
// per round) — there is no single "advance" event worth reacting to
// immediately. A slow poll is the right shape; the seed's own value is only
// ever a lagging, best-effort signal for future committee selection anyway
// (internal/reputation's own package doc: "never feeds the live 2f+1 quorum
// tally").
const reputationPushDefaultIntervalSeconds = 300 // 5 minutes

// reputationPushIntervalSeconds is env-overridable via
// JMDN_REPUTATION_PUSH_INTERVAL_SECONDS for operators who want a tighter or
// looser cadence; envUint64 is shared with vote_crdt_compaction.go.
func reputationPushIntervalSeconds() uint64 {
	return envUint64("JMDN_REPUTATION_PUSH_INTERVAL_SECONDS", reputationPushDefaultIntervalSeconds)
}

// equivocationPushDebounce (A4-COMPLETION-LLD.md §5) coalesces a burst of
// equivocations found in the same ConvergeAndCompact pass into one
// out-of-band push, not one per fault — same reasoning as
// voteCRDTCompactionDebounce in vote_crdt_compaction.go, deliberately
// longer than that 750ms since a push is a full seed round-trip per peer,
// not a single local trigger signal.
const equivocationPushDebounce = 2 * time.Second

// immediateReputationPush is signalled by triggerImmediateReputationPush to
// request an out-of-band push sooner than the next regular tick. Buffered 1
// and only ever written to via the non-blocking select below: a signal
// arriving while one is already pending is simply coalesced, never queued
// or blocked on.
var immediateReputationPush = make(chan struct{}, 1)

// reputationPusherRunning tracks whether startReputationSeedPusher's worker
// goroutine is actually receiving from immediateReputationPush. Before that
// (or on a node with no seed configured, where the worker never starts at
// all), triggerImmediateReputationPush's send would sit in the buffer
// forever with nothing to coalesce against on a repeat call — harmless
// either way since ReportEquivocation only ever fires after full node
// startup in practice, but checked explicitly so a call before startup is a
// documented no-op rather than an unexplained dropped signal.
var reputationPusherRunning atomic.Bool

// triggerImmediateReputationPush requests an out-of-band reputation push as
// soon as the debounce window settles, instead of waiting for the next
// regular tick (up to reputationPushIntervalSeconds later). Non-blocking,
// safe to call from any goroutine — in practice, from
// equivocationReputationReporter.ReportEquivocation, which itself runs
// inside ConvergeAndCompact's evaluation loop and must never block on this.
func triggerImmediateReputationPush() {
	if !reputationPusherRunning.Load() {
		return
	}
	select {
	case immediateReputationPush <- struct{}{}:
	default: // already pending, coalesce
	}
}

// startReputationSeedPusher launches the background ticker plus the
// immediate-push worker. seedClient may be nil (no seed node configured);
// the loop then logs once and exits, mirroring how the sync-monitor block
// already treats an absent seed as optional rather than fatal. On every
// tick — regular or immediate — it is always safe to call even on a
// non-sequencer node: PushReputationWeights internally checks for a
// registered sequencer sign key and no-ops (cheap, no RPCs) when this node
// isn't the sequencer.
func startReputationSeedPusher(ctx context.Context, seedClient reputationPusher) {
	if seedClient == nil {
		log.Info().Msg("[ReputationPush] no seednode client configured — reputation push disabled")
		return
	}
	interval := time.Duration(reputationPushIntervalSeconds()) * time.Second

	go func() {
		reputationPusherRunning.Store(true)
		defer reputationPusherRunning.Store(false)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pushReputationOnce(ctx, seedClient)
			case <-immediateReputationPush:
				select {
				case <-ctx.Done():
					return
				case <-time.After(equivocationPushDebounce):
				}
				pushReputationOnce(ctx, seedClient)
			}
		}
	}()
	log.Info().Dur("interval", interval).Msg("[ReputationPush] background pusher started")
}

// pushReputationOnce is the testable core of one tick (regular or
// immediate): snapshot, remap, push, log. Split out so a test can call it
// directly without waiting on a ticker.
func pushReputationOnce(ctx context.Context, seedClient reputationPusher) {
	if !reputation.Enabled {
		return
	}
	// A4-COMPLETION-LLD.md §3.4's ordering mechanism: label this node's own
	// metrics with whether it's the sequencer, independent of whether there's
	// anything to push this tick -- IsSequencer() is a cheap local check
	// (currentSequencerSignKey() != nil), no RPC involved.
	if seednode.IsSequencer() {
		metrics.ReputationNodeIsSequencerGauge.Set(1)
	} else {
		metrics.ReputationNodeIsSequencerGauge.Set(0)
	}
	weights := reputation.SnapshotSelectionWeights()
	if len(weights) == 0 {
		return // nothing observed yet this run
	}
	accepted, failures := seedClient.PushReputationWeights(ctx, weights)
	if len(failures) > 0 {
		// ErrNotSequencer is the overwhelmingly common case on non-sequencer
		// nodes and every tick would otherwise log it forever at warn level —
		// keep that one quiet (debug), surface anything else (a real RPC
		// failure) at warn. Checked by identity (errors.Is), never by
		// counting or string content, so a genuine single-peer RPC failure
		// is never mistaken for "not the sequencer".
		if len(failures) == 1 && errors.Is(failures[0], seednode.ErrNotSequencer) {
			log.Debug().Msg("[ReputationPush] skipped this tick — not the sequencer")
			return
		}
		log.Warn().Int("accepted", accepted).Int("failed", len(failures)).Errs("errors", failures).
			Msg("[ReputationPush] some peer weight updates failed")
		return
	}
	log.Debug().Int("peers", accepted).Msg("[ReputationPush] pushed reputation weights to seed")
}
