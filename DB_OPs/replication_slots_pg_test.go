//go:build pgtest

package DB_OPs

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// InspectReplicationSlots runs against a real server and reports the cap as a
// non-empty setting string, whatever the server's slots are.
func TestInspectReplicationSlots_PG(t *testing.T) {
	db, err := sql.Open("postgres", pgDSN(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slots, maxKeep, err := InspectReplicationSlots(ctx, db)
	if err != nil {
		t.Fatalf("InspectReplicationSlots: %v", err)
	}
	if maxKeep == "" {
		t.Fatal("max_slot_wal_keep_size came back empty")
	}
	for _, s := range slots {
		if s.Name == "" || s.HeldBytes < 0 {
			t.Errorf("bad slot row: %+v", s)
		}
	}
}
