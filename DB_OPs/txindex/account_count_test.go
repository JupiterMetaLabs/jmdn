package txindex

import (
	"context"
	"path/filepath"
	"testing"
)

// The account/DID counter: unseeded → increments are no-ops; after a one-time
// seed, increments accumulate; re-seed overwrites.
func TestAccountCount_SeedIncrGet(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "acct.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Unseeded: reports not-seeded, no error.
	if _, seeded, err := db.GetAccountCount(ctx); err != nil || seeded {
		t.Fatalf("unseeded: want (_, false, nil); got seeded=%v err=%v", seeded, err)
	}

	// Increment BEFORE seed must not create a bogus partial counter.
	if err := db.IncrAccountCount(ctx, 5); err != nil {
		t.Fatalf("incr pre-seed: %v", err)
	}
	if _, seeded, _ := db.GetAccountCount(ctx); seeded {
		t.Fatal("increment before seed must remain a no-op (counter still unseeded)")
	}

	// One-time seed, then read back.
	if err := db.SetAccountCount(ctx, 100); err != nil {
		t.Fatalf("seed: %v", err)
	}
	n, seeded, err := db.GetAccountCount(ctx)
	if err != nil || !seeded || n != 100 {
		t.Fatalf("after seed: want (100, true, nil); got (%d, %v, %v)", n, seeded, err)
	}

	// Increments after seed accumulate.
	if err := db.IncrAccountCount(ctx, 3); err != nil {
		t.Fatalf("incr +3: %v", err)
	}
	if err := db.IncrAccountCount(ctx, 1); err != nil {
		t.Fatalf("incr +1: %v", err)
	}
	if n, _, _ := db.GetAccountCount(ctx); n != 104 {
		t.Fatalf("after +4: want 104; got %d", n)
	}

	// delta 0 is a no-op.
	if err := db.IncrAccountCount(ctx, 0); err != nil {
		t.Fatalf("incr 0: %v", err)
	}
	if n, _, _ := db.GetAccountCount(ctx); n != 104 {
		t.Fatalf("after +0: want 104; got %d", n)
	}

	// Re-seed overwrites (used if the counter is ever reset for a re-index).
	if err := db.SetAccountCount(ctx, 50); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if n, _, _ := db.GetAccountCount(ctx); n != 50 {
		t.Fatalf("after reseed: want 50; got %d", n)
	}
}
