package BlockProcessing

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The per-block-hash apply lock must serialize concurrent applies of the SAME
// block (the fix for double-credited, divergent balances under multi-transport
// delivery), let DIFFERENT block hashes proceed in parallel, and not leak.

func TestBlockApplyLock_SerializesSameHash(t *testing.T) {
	const goroutines = 64
	var inSection int32
	var maxConcurrent int32
	var counter int // non-atomic on purpose: only correct if the section is serialized
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := acquireBlockApplyLock("0xsameblock")
			defer release()

			cur := atomic.AddInt32(&inSection, 1)
			for {
				m := atomic.LoadInt32(&maxConcurrent)
				if cur <= m || atomic.CompareAndSwapInt32(&maxConcurrent, m, cur) {
					break
				}
			}
			counter++ // a lost update here proves the lock did NOT serialize
			time.Sleep(200 * time.Microsecond)
			atomic.AddInt32(&inSection, -1)
		}()
	}
	wg.Wait()

	if maxConcurrent != 1 {
		t.Fatalf("same-hash apply lock must serialize: observed %d concurrent, want 1", maxConcurrent)
	}
	if counter != goroutines {
		t.Fatalf("non-atomic counter lost updates (%d/%d) — the lock is not serializing same-hash applies", counter, goroutines)
	}
}

func TestBlockApplyLock_DifferentHashesRunInParallel(t *testing.T) {
	relA := acquireBlockApplyLock("0xA")
	acquiredB := make(chan struct{})
	go func() {
		relB := acquireBlockApplyLock("0xB")
		close(acquiredB)
		relB()
	}()
	select {
	case <-acquiredB: // good: 0xB acquired while 0xA is still held
	case <-time.After(2 * time.Second):
		relA()
		t.Fatal("a different block hash must not block on another's apply lock")
	}
	relA()
}

func TestBlockApplyLock_NoLeak(t *testing.T) {
	rel := acquireBlockApplyLock("0xtransient")
	rel()
	blockApplyLocksMu.Lock()
	_, present := blockApplyLocks["0xtransient"]
	blockApplyLocksMu.Unlock()
	if present {
		t.Fatal("apply-lock entry was not cleaned up after release (leak)")
	}
}
