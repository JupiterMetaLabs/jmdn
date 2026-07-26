package main

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Event-driven seednode head reporting.
//
// The sync monitor reports this node's latest block to the seednode on a
// periodic, adaptive timer (1–30 min). That makes the seednode's per-node
// block_head — and the ops "latest block per endpoint" view it feeds — lag real
// state by minutes. This file adds an ADDITIVE push: right after a block's state
// is committed (DB_OPs.UpdateLatestBlockMonotonic advances), fire an immediate
// Monitor.TriggerCheck so the seednode learns the new head within ~1s. The
// periodic timer stays as the backstop for missed events / seednode outages.

// seedBlockHeadPushDebounce coalesces a burst of applied blocks (e.g. a catch-up
// applying many blocks back-to-back) into a single seednode report of the final
// head. TriggerCheck re-reads the current head, so one trailing call suffices.
const seedBlockHeadPushDebounce = 750 * time.Millisecond

// startSeedBlockHeadPusher launches the background pusher with the production
// debounce window and returns the fire-and-forget hook to hand to
// DB_OPs.SetLatestBlockAdvanceHook. trigger is normally syncMonitor.TriggerCheck.
func startSeedBlockHeadPusher(ctx context.Context, trigger func(context.Context)) func(uint64) {
	return newSeedBlockHeadPusher(ctx, trigger, seedBlockHeadPushDebounce)
}

// newSeedBlockHeadPusher is the testable core: it returns a hook that only
// records the latest head and signals a worker goroutine. The worker debounces,
// then invokes trigger exactly once per quiet window. The hook never blocks and
// never calls trigger inline, so it is safe to run under DB_OPs' latestBlockMu.
// A nil trigger yields a no-op hook (node without a sync monitor).
func newSeedBlockHeadPusher(ctx context.Context, trigger func(context.Context), debounce time.Duration) func(uint64) {
	if trigger == nil {
		return func(uint64) {}
	}

	var pending atomic.Bool
	var lastHead atomic.Uint64
	wake := make(chan struct{}, 1)

	hook := func(head uint64) {
		lastHead.Store(head)
		pending.Store(true)
		select {
		case wake <- struct{}{}: // signal the worker
		default: // already signalled — coalesce
		}
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			// Let a burst settle before reporting once.
			select {
			case <-ctx.Done():
				return
			case <-time.After(debounce):
			}
			if pending.Swap(false) {
				trigger(ctx)
				log.Debug().
					Uint64("head", lastHead.Load()).
					Msg("[SeedPush] pushed head to seednode after block apply")
			}
		}
	}()

	return hook
}
