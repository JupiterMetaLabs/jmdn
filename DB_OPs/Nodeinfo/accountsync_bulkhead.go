package NodeInfo

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

// MODULE: DB_OPs/Nodeinfo (account-sync scan bulkhead)
// PURPOSE: cap how many accounts-DB pool connections the FastSync account-sync
// SERVING path may hold at once, so a peer syncing from this node can never
// starve JSON-RPC, block commit or consensus of accounts-DB connections.
//
// Why it is needed: JMDN-FastSync's AccountDispatcher runs DispatchWorkers (10)
// workers per session, plus one diff producer, and every worker page calls
// immudbNonceIter.GetAccountsByNonces, which loops ListAccountsPaginatedFrom
// (one pool Get per 1,000 accounts) with no context. One session therefore
// keeps ~11 pool connections busy back-to-back; two or three sessions, plus the
// connections that main/DID hold for the life of the process, exhaust the
// 30-connection pool. The pool's Get never blocks, so every other caller then
// fails instantly with config.ErrMaxConnectionsReached.
//
// The limiter is process-wide (all sessions share it). It does not make a sync
// session faster; it bounds the damage a slow one can do.
//
// DO NOT:
//   - Acquire a slot and then call anything that acquires a slot again (the
//     limiter is not re-entrant; it would self-deadlock at capacity 1).
//   - Hold a slot across anything other than one ListAccountsPaginatedFrom call.

// accountSyncDBSlotsEnv overrides the number of concurrent account-sync scans.
const accountSyncDBSlotsEnv = "JMDN_ACCOUNTSYNC_DB_SLOTS"

const (
	defaultAccountSyncDBSlots = 4
	maxAccountSyncDBSlots     = 16
)

// Retry schedule for pool exhaustion inside one slot: 50,100,200,400,800 ms
// (≈1.55 s total) before the error is surfaced to the dispatcher, which would
// otherwise burn its 3 retries in ~1 ms and dead-letter the page.
const (
	poolBusyMaxAttempts = 6
	poolBusyBaseBackoff = 50 * time.Millisecond
)

// scanLimiter is a counting semaphore. Space: O(capacity).
type scanLimiter struct {
	slots   chan struct{}
	backoff func(attempt int) time.Duration
	sleep   func(time.Duration)
}

func newScanLimiter(capacity int) *scanLimiter {
	if capacity < 1 {
		capacity = 1
	}
	return &scanLimiter{
		slots: make(chan struct{}, capacity),
		backoff: func(attempt int) time.Duration {
			return poolBusyBaseBackoff << attempt
		},
		sleep: time.Sleep,
	}
}

// do runs fn while holding one slot. If fn fails because the accounts pool is
// exhausted it is retried with exponential backoff, still inside the slot, up
// to poolBusyMaxAttempts times. Any other error is returned immediately.
//
// Blocks while all slots are taken: the FastSync callbacks that reach this
// path carry no context, so waiting is the back-pressure.
// Time: O(attempts) calls of fn.
func (l *scanLimiter) do(fn func() error) error {
	l.slots <- struct{}{}
	defer func() { <-l.slots }()

	var err error
	for attempt := 0; attempt < poolBusyMaxAttempts; attempt++ {
		if err = fn(); err == nil || !errors.Is(err, config.ErrMaxConnectionsReached) {
			return err
		}
		if attempt < poolBusyMaxAttempts-1 {
			l.sleep(l.backoff(attempt))
		}
	}
	return err
}

func accountSyncDBSlots() int {
	raw := strings.TrimSpace(os.Getenv(accountSyncDBSlotsEnv))
	if raw == "" {
		return defaultAccountSyncDBSlots
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > maxAccountSyncDBSlots {
		return defaultAccountSyncDBSlots
	}
	return v
}

// accountSyncScan is shared by every immudbNonceIter in the process.
var accountSyncScan = newScanLimiter(accountSyncDBSlots())

// listAccountsForSync is ListAccountsPaginatedFrom behind the bulkhead.
// Time: O(limit) ImmuDB entries; Space: O(limit).
func listAccountsForSync(limit int, seekKey []byte) ([]*DB_OPs.Account, []byte, error) {
	var (
		accs    []*DB_OPs.Account
		lastKey []byte
	)
	err := accountSyncScan.do(func() error {
		var e error
		accs, lastKey, e = DB_OPs.ListAccountsPaginatedFrom(nil, limit, seekKey, "")
		return e
	})
	return accs, lastKey, err
}
