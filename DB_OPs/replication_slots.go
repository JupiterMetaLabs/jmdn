package DB_OPs

import (
	"context"
	"database/sql"
	"fmt"
)

// ReplicationSlot is one row of pg_replication_slots.
type ReplicationSlot struct {
	Name   string
	Active bool
	// HeldBytes is the WAL Postgres keeps for this slot (current LSN minus
	// restart_lsn). Zero when unknown: no restart_lsn yet, or the server is a
	// standby where pg_current_wal_lsn() is unavailable.
	HeldBytes int64
}

// ReplicationSlotWarning is a slot or setting that lets Postgres keep WAL
// without bound. Slot is empty for the max_slot_wal_keep_size warning.
type ReplicationSlotWarning struct {
	Slot      string
	HeldBytes int64
	Message   string
}

// UnlimitedSlotWALKeepSize is the value Postgres reports for
// max_slot_wal_keep_size when slots may retain WAL without limit (the default).
const UnlimitedSlotWALKeepSize = "-1"

const replicationSlotsQuery = `
SELECT slot_name,
       active,
       CASE WHEN pg_is_in_recovery() OR restart_lsn IS NULL THEN 0
            ELSE pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)::bigint
       END
FROM pg_replication_slots
ORDER BY slot_name`

// InspectReplicationSlots returns every replication slot on the server and the
// current max_slot_wal_keep_size setting (for example "2GB", or "-1" when
// unlimited). Both are readable by a non-superuser role.
func InspectReplicationSlots(ctx context.Context, db *sql.DB) ([]ReplicationSlot, string, error) {
	var maxKeep string
	if err := db.QueryRowContext(ctx, `SELECT current_setting('max_slot_wal_keep_size')`).Scan(&maxKeep); err != nil {
		return nil, "", fmt.Errorf("read max_slot_wal_keep_size: %w", err)
	}

	rows, err := db.QueryContext(ctx, replicationSlotsQuery)
	if err != nil {
		return nil, maxKeep, fmt.Errorf("read pg_replication_slots: %w", err)
	}
	defer rows.Close()

	var slots []ReplicationSlot
	for rows.Next() {
		var s ReplicationSlot
		if err := rows.Scan(&s.Name, &s.Active, &s.HeldBytes); err != nil {
			return nil, maxKeep, fmt.Errorf("scan pg_replication_slots: %w", err)
		}
		slots = append(slots, s)
	}
	if err := rows.Err(); err != nil {
		return nil, maxKeep, fmt.Errorf("read pg_replication_slots: %w", err)
	}
	return slots, maxKeep, nil
}

// ReplicationSlotWarnings decides what to report at boot.
//
// An inactive slot is reported unless it is the slot this node's CDC pipeline
// is about to attach to (cdcEnabled and name == cdcSlot). Postgres keeps every
// WAL segment written after an inactive slot's restart_lsn, so a slot nothing
// reads grows until max_slot_wal_keep_size or the disk runs out.
//
// An unlimited max_slot_wal_keep_size is always reported, because then any
// forgotten slot can fill the disk.
func ReplicationSlotWarnings(slots []ReplicationSlot, maxKeep string, cdcEnabled bool, cdcSlot string) []ReplicationSlotWarning {
	var out []ReplicationSlotWarning
	for _, s := range slots {
		if s.Active {
			continue
		}
		if cdcEnabled && s.Name == cdcSlot {
			continue
		}
		msg := fmt.Sprintf("replication slot %q is inactive and retains WAL; nothing in this node reads it", s.Name)
		if !cdcEnabled && s.Name == cdcSlot {
			msg = fmt.Sprintf("replication slot %q belongs to the CDC pipeline, but thebe.cdc.enabled is false, so nothing reads it", s.Name)
		}
		out = append(out, ReplicationSlotWarning{
			Slot:      s.Name,
			HeldBytes: s.HeldBytes,
			Message:   msg + fmt.Sprintf("; if no other consumer uses it, drop it: SELECT pg_drop_replication_slot('%s');", s.Name),
		})
	}
	if maxKeep == UnlimitedSlotWALKeepSize {
		out = append(out, ReplicationSlotWarning{
			Message: "max_slot_wal_keep_size is unlimited (-1), so a stalled or abandoned replication slot can fill the disk; " +
				"set a cap as a superuser: ALTER SYSTEM SET max_slot_wal_keep_size = '2GB'; SELECT pg_reload_conf();",
		})
	}
	return out
}
