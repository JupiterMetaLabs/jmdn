//go:build pgtest

package DB_OPs

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("JMDN_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("set JMDN_TEST_PG_DSN to run Postgres-backed tests")
	}
	return dsn + " dbname=postgres"
}

type loadResult struct {
	readP99    time.Duration
	readWaits  int64
	syncPeak   int
	syncWaits  int64
	readErrors int64
}

// runLoad saturates syncDB with `sessions` long scans (pg_sleep stands in for a
// slow page) while 4 RPC callers issue short reads on readDB for `dur`.
func runLoad(t *testing.T, syncDB, readDB *sql.DB, sessions int, dur time.Duration) loadResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_, _ = syncDB.ExecContext(ctx, "SELECT pg_sleep(0.2)")
			}
		}()
	}
	var peak int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				if n := int64(syncDB.Stats().InUse); n > atomic.LoadInt64(&peak) {
					atomic.StoreInt64(&peak, n)
				}
			}
		}
	}()
	readWait0 := readDB.Stats().WaitCount
	var mu sync.Mutex
	var lat []time.Duration
	var rerr int64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				s := time.Now()
				var one int
				// RPC-like read with the same 5 s budget as DB_OPs.GetAccount.
				qctx, qcancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := readDB.QueryRowContext(qctx, "SELECT 1").Scan(&one)
				qcancel()
				d := time.Since(s)
				mu.Lock()
				lat = append(lat, d)
				mu.Unlock()
				if err != nil {
					atomic.AddInt64(&rerr, 1)
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	close(stop)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p99 := time.Duration(0)
	if len(lat) > 0 {
		p99 = lat[len(lat)*99/100]
	}
	return loadResult{
		readP99:    p99,
		readWaits:  readDB.Stats().WaitCount - readWait0,
		syncPeak:   int(atomic.LoadInt64(&peak)),
		syncWaits:  syncDB.Stats().WaitCount,
		readErrors: rerr,
	}
}

// Isolation: with sync on its own budget (2), 12 concurrent sync scans never
// hold more than 2 connections and RPC reads on the read pool never wait.
func TestTaskPools_SyncLoadCannotStarveReads(t *testing.T) {
	dsn := pgDSN(t)
	ctx := context.Background()
	syncDB, err := OpenTaskDB(ctx, TaskSync, dsn, TaskPoolOptions{MaxOpen: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer syncDB.Close()
	readDB, err := OpenTaskDB(ctx, TaskRead, dsn, TaskPoolOptions{MaxOpen: 4}) // = number of RPC callers, so any wait would be sync-induced
	if err != nil {
		t.Fatal(err)
	}
	defer readDB.Close()

	r := runLoad(t, syncDB, readDB, 12, 1500*time.Millisecond)
	t.Logf("task pools: sync peak in-use=%d (budget 2), sync waits=%d; read p99=%v, read waits=%d, read errors=%d",
		r.syncPeak, r.syncWaits, r.readP99, r.readWaits, r.readErrors)
	if r.syncPeak > 2 {
		t.Fatalf("sync exceeded its budget: peak %d > 2", r.syncPeak)
	}
	if r.syncWaits == 0 {
		t.Fatal("precondition: sync load did not saturate its pool")
	}
	if r.readWaits != 0 || r.readErrors != 0 {
		t.Fatalf("RPC reads were affected by sync load: waits=%d errors=%d", r.readWaits, r.readErrors)
	}
	if r.readP99 > 100*time.Millisecond {
		t.Fatalf("RPC read p99 %v > 100ms under sync load", r.readP99)
	}
}

// Contrast (what v3base does today): one shared pool of a similar total size (5) —
// sync load makes RPC reads queue behind 200 ms scans.
func TestTaskPools_SharedPoolBaselineStarvesReads(t *testing.T) {
	dsn := pgDSN(t)
	shared, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	shared.SetMaxOpenConns(5)
	r := runLoad(t, shared, shared, 12, 1500*time.Millisecond)
	t.Logf("shared pool (5): read p99=%v, read waits=%d", r.readP99, r.readWaits)
	if r.readP99 < 100*time.Millisecond {
		t.Fatalf("baseline expected to show starvation (p99 >= 100ms), got %v", r.readP99)
	}
}
