package Service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"gossipnode/gETH/Facade/Service/Types"
)

// fakeFates reports a block stored once it has been marked, pending before,
// and gone if a different hash was marked at its height.
type fakeFates struct {
	mu     sync.Mutex
	stored map[uint64]ethcommon.Hash
}

func (f *fakeFates) mark(h uint64, hash ethcommon.Hash) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored[h] = hash
}

func (f *fakeFates) fate(k logBlockKey) logBlockFate {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.stored[k.height]
	switch {
	case !ok:
		return logBlockPending
	case h == k.hash:
		return logBlockStored
	default:
		return logBlockGone
	}
}

// emit mimics LogWriter.fanOut: a non-blocking send that drops when full.
func emit(ch chan *ethtypes.Log, l *ethtypes.Log, drops *atomic.Int64) {
	select {
	case ch <- l:
	default:
		drops.Add(1)
	}
}

func collect(out <-chan Types.Log, want int, within time.Duration) int {
	got := 0
	deadline := time.After(within)
	for got < want {
		select {
		case <-out:
			got++
		case <-deadline:
			return got
		}
	}
	return got
}

// A block with far more logs than the fan-out buffer, emitted while the block
// is being applied and stored only afterwards, must be delivered in full.
// Before the fix the reader blocked on the first log's block and the fan-out
// dropped everything past its 256-entry buffer (257 of 400 delivered).
func TestForwardStoredLogs_BusyBlockLosesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := make(chan *ethtypes.Log, 256)
	out := make(chan Types.Log, 100)
	fates := &fakeFates{stored: map[uint64]ethcommon.Hash{}}
	go forwardStoredLogs(ctx, raw, out, nil, fates.fate, 10*time.Millisecond, 5*time.Second)

	hash := ethcommon.HexToHash("0xb1")
	var drops atomic.Int64
	for tx := 0; tx < 40; tx++ {
		for i := 0; i < 10; i++ {
			emit(raw, &ethtypes.Log{BlockNumber: 7, BlockHash: hash, TxIndex: uint(tx), Index: uint(tx*10 + i)}, &drops)
		}
		time.Sleep(5 * time.Millisecond)
	}
	fates.mark(7, hash)

	if got := collect(out, 400, 5*time.Second); got != 400 || drops.Load() != 0 {
		t.Fatalf("delivered %d of 400, fan-out dropped %d", got, drops.Load())
	}
}

// A block that is never stored costs one wait in total, not one per log, and
// the next block's logs are delivered afterwards.
func TestForwardStoredLogs_NeverStoredBlockIsDroppedOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := make(chan *ethtypes.Log, 256)
	out := make(chan Types.Log, 100)
	fates := &fakeFates{stored: map[uint64]ethcommon.Hash{}}
	const wait = 300 * time.Millisecond
	go forwardStoredLogs(ctx, raw, out, nil, fates.fate, 10*time.Millisecond, wait)

	rolledBack := ethcommon.HexToHash("0xdead")
	var drops atomic.Int64
	for i := 0; i < 50; i++ {
		emit(raw, &ethtypes.Log{BlockNumber: 8, BlockHash: rolledBack, Index: uint(i)}, &drops)
	}
	next := ethcommon.HexToHash("0xb9")
	for i := 0; i < 5; i++ {
		emit(raw, &ethtypes.Log{BlockNumber: 9, BlockHash: next, Index: uint(i)}, &drops)
	}
	fates.mark(9, next)

	start := time.Now()
	got := collect(out, 5, 3*time.Second)
	if got != 5 {
		t.Fatalf("delivered %d of the next block's 5 logs", got)
	}
	if elapsed := time.Since(start); elapsed > 3*wait {
		t.Fatalf("next block waited %v; a never-stored block must cost one wait (%v), not one per log", elapsed, wait)
	}
}

// Logs of a competing block at a height whose stored block is different are
// dropped as soon as that is known.
func TestForwardStoredLogs_CompetingBlockDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := make(chan *ethtypes.Log, 256)
	out := make(chan Types.Log, 100)
	fates := &fakeFates{stored: map[uint64]ethcommon.Hash{}}
	go forwardStoredLogs(ctx, raw, out, nil, fates.fate, 10*time.Millisecond, 5*time.Second)

	canonical, competing := ethcommon.HexToHash("0xc1"), ethcommon.HexToHash("0xc2")
	fates.mark(10, canonical)
	var drops atomic.Int64
	emit(raw, &ethtypes.Log{BlockNumber: 10, BlockHash: competing}, &drops)
	emit(raw, &ethtypes.Log{BlockNumber: 10, BlockHash: canonical}, &drops)

	if got := collect(out, 1, 2*time.Second); got != 1 {
		t.Fatalf("want exactly the canonical block's log, got %d", got)
	}
	select {
	case l := <-out:
		t.Fatalf("a competing block's log was delivered: %+v", l)
	case <-time.After(100 * time.Millisecond):
	}
}

// A consumer that stops taking logs while they are ready ends the
// subscription after the slow-consumer timeout instead of holding logs and
// rechecking them for ever.
func TestForwardStoredLogs_StuckConsumerIsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := make(chan *ethtypes.Log, 256)
	out := make(chan Types.Log) // never read
	fates := &fakeFates{stored: map[uint64]ethcommon.Hash{}}
	hash := ethcommon.HexToHash("0xa1")
	fates.mark(3, hash)
	done := make(chan struct{})
	go func() {
		forwardStoredLogsLimits(ctx, raw, out, nil, fates.fate, 10*time.Millisecond, time.Second, 1000, 200*time.Millisecond)
		close(done)
	}()
	for i := 0; i < 10; i++ {
		raw <- &ethtypes.Log{BlockNumber: 3, BlockHash: hash, Index: uint(i)}
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription with a stuck consumer was not closed")
	}
}

// More than maxHeld logs closes the subscription rather than dropping logs
// silently.
func TestForwardStoredLogs_OverflowClosesSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := make(chan *ethtypes.Log, 256)
	out := make(chan Types.Log)
	fates := &fakeFates{stored: map[uint64]ethcommon.Hash{}} // nothing stored: all pending
	done := make(chan struct{})
	go func() {
		forwardStoredLogsLimits(ctx, raw, out, nil, fates.fate, 10*time.Millisecond, time.Minute, 50, time.Minute)
		close(done)
	}()
	hash := ethcommon.HexToHash("0xa2")
	for i := 0; i < 51; i++ {
		raw <- &ethtypes.Log{BlockNumber: 4, BlockHash: hash, Index: uint(i)}
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription over its cap was not closed")
	}
}

// Held logs are checked once per pending block per interval, never per log,
// and blocks already known stored are not checked again: a full queue of
// stored logs costs nothing per tick.
func TestForwardStoredLogs_ChecksBlocksNotLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := make(chan *ethtypes.Log, 256)
	out := make(chan Types.Log) // never read
	hash := ethcommon.HexToHash("0xa3")
	var calls atomic.Int64
	fate := func(k logBlockKey) logBlockFate {
		calls.Add(1)
		if k.hash == hash {
			return logBlockStored
		}
		return logBlockPending
	}
	go forwardStoredLogsLimits(ctx, raw, out, nil, fate, 5*time.Millisecond, time.Minute, 100000, time.Minute)
	for i := 0; i < 20000; i++ {
		raw <- &ethtypes.Log{BlockNumber: 5, BlockHash: hash, Index: uint(i)}
	}
	time.Sleep(200 * time.Millisecond) // ~40 ticks
	if n := calls.Load(); n != 1 {
		t.Fatalf("fate checked %d times for one stored block; want 1", n)
	}
}
