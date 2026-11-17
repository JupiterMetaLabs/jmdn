//go:build pgtest

//	JMDN_TEST_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
//	  go test -tags pgtest ./DB_OPs/thebegateway/ -run TestGetBlockHashes -v

package thebegateway_test

import (
	"context"
	"fmt"
	"testing"

	"gossipnode/DB_OPs/thebegateway"
)

// GetBlockHashes returns the hash of each listed height that has a block,
// leaves out heights without one, and reads only the listed rows.
func TestGetBlockHashes(t *testing.T) {
	db := freshDB(t)
	hashOf := func(n int) string { return fmt.Sprintf("0x%064x", n+7) }
	for _, n := range []int{1, 5, 9, 1_000_000} {
		if _, err := db.Exec(`INSERT INTO blocks (block_number, block_hash, parent_hash, timestamp, txs_root, state_root, status)
			VALUES ($1, $2, $2, NOW(), $2, $2, 1)`, n, hashOf(n)); err != nil {
			t.Fatal(err)
		}
	}
	r := thebegateway.NewThebeReader(db, nil, nil)
	got, err := r.GetBlockHashes(context.Background(), []uint64{1, 2, 9, 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint64]string{1: hashOf(1), 9: hashOf(9), 1_000_000: hashOf(1_000_000)}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for n, h := range want {
		if got[n] != h {
			t.Fatalf("height %d: got %q, want %q", n, got[n], h)
		}
	}
	if empty, err := r.GetBlockHashes(context.Background(), nil); err != nil || len(empty) != 0 {
		t.Fatalf("no heights: got %v, %v", empty, err)
	}
}
