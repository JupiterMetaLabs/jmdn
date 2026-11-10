//go:build applygate

package thebeprofile_test

// A legacy block that repeats an earlier block's transactions carries the same
// block hash, transactions root and (when the repeat changes nothing) state
// root. The projection must store both blocks, and a lookup by hash must return
// the earlier one on every node.
//
//	APPLYGATE_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
//	  go test -tags applygate ./DB_OPs/Tests/thebeprofile/ -run TestBlockHashRepeat

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"
	_ "github.com/lib/pq"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/DB_OPs/thebeprofile"
	"gossipnode/internal/applygate"
)

func record(t *testing.T, ns string, v any) *core.CanonicalRecord {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return &core.CanonicalRecord{Namespace: ns, Value: b}
}

func TestBlockHashRepeat(t *testing.T) {
	db, err := sql.Open("postgres", applygate.FreshPGDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	p := thebeprofile.NewJMDNProfile()

	if _, err := db.ExecContext(ctx, p.GetMigration()); err != nil {
		t.Fatalf("migration: %v", err)
	}
	// A database created by an earlier build has the unique keys; the next boot
	// must drop them.
	for _, ddl := range []string{
		`ALTER TABLE blocks ADD CONSTRAINT blocks_block_hash_key UNIQUE (block_hash)`,
		`ALTER TABLE blocks ADD CONSTRAINT blocks_txs_root_key UNIQUE (txs_root)`,
		`ALTER TABLE blocks ADD CONSTRAINT blocks_state_root_key UNIQUE (state_root)`,
		`ALTER TABLE snapshots ADD CONSTRAINT snapshots_block_hash_key UNIQUE (block_hash)`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	if _, err := db.ExecContext(ctx, p.GetMigration()); err != nil {
		t.Fatalf("migration on an earlier database: %v", err)
	}
	var unique int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint
		WHERE contype = 'u' AND conrelid IN ('blocks'::regclass, 'snapshots'::regclass)`).Scan(&unique); err != nil {
		t.Fatal(err)
	}
	if unique != 0 {
		t.Fatalf("%d unique keys left on blocks/snapshots", unique)
	}

	const hash = "0x43b684ae08e839130871f2132536a1844ee703ce5765aa89ba0ee310f3a27ff1"
	const root = "0x1111111111111111111111111111111111111111111111111111111111111111"
	const state = "0x2222222222222222222222222222222222222222222222222222222222222222"
	ts := time.Unix(1_760_000_000, 0).UTC()
	// The repeat (11) is written first, so a lookup without a defined order
	// would tend to return it.
	for seq, n := range []uint64{11, 10} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		blk := thebegateway.BlockRecord{
			BlockNumber: n, BlockHash: hash, ParentHash: root, Timestamp: ts.Add(time.Duration(n) * time.Second),
			TxsRoot: root, StateRoot: state, Status: 1,
		}
		snap := thebegateway.SnapshotRecord{BlockNumber: n, BlockHash: hash, CreatedAt: ts}
		if err := p.Apply(ctx, uint64(2*seq+1), record(t, "block", blk), tx); err != nil {
			_ = tx.Rollback()
			t.Fatalf("block %d: %v", n, err)
		}
		if err := p.Apply(ctx, uint64(2*seq+2), record(t, "snapshot", snap), tx); err != nil {
			_ = tx.Rollback()
			t.Fatalf("snapshot %d: %v", n, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var stored int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM blocks WHERE block_hash = $1`, hash).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 2 {
		t.Fatalf("stored %d blocks with the repeated hash, want 2", stored)
	}

	r := thebegateway.NewThebeReader(db, nil, nil)
	got, err := r.GetBlockByHash(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.BlockNumber != 10 {
		t.Fatalf("GetBlockByHash returned block %d, want the earlier block 10", got.BlockNumber)
	}
}
