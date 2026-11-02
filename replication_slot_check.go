package main

import (
	"context"
	"database/sql"
	"time"

	"gossipnode/DB_OPs"

	ion "github.com/JupiterMetaLabs/ion"
)

// reportReplicationSlots logs, at boot after CDC started, a CDC slot that is
// inactive, lagging or invalidated, any other replication slot nothing on this
// node reads, and an unlimited max_slot_wal_keep_size. It never fails boot: a
// failed check is logged and skipped.
func reportReplicationSlots(db *sql.DB, cdcEnabled bool, cdcSlot string, lagBytes int64) {
	logger := mainLogger()
	if logger == nil || db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if cdcSlot == "" {
		cdcSlot = "thebe_eventlog" // ThebeDB's default slot name
	}
	slots, maxKeep, err := DB_OPs.InspectReplicationSlots(ctx, db)
	if err != nil {
		logger.Warn(ctx, "thebedb: could not check Postgres replication slots",
			ion.Err(err),
			ion.String("function", "main.reportReplicationSlots"))
		return
	}
	for _, w := range DB_OPs.ReplicationSlotWarnings(slots, maxKeep, cdcEnabled, cdcSlot, lagBytes) {
		logger.Warn(ctx, "thebedb: "+w.Message,
			ion.String("slot", w.Slot),
			ion.Int64("wal_held_bytes", w.HeldBytes),
			ion.String("max_slot_wal_keep_size", maxKeep),
			ion.Bool("cdc_enabled", cdcEnabled),
			ion.String("function", "main.reportReplicationSlots"))
	}
}
