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
	// WALStatus is pg_replication_slots.wal_status: "reserved", "extended",
	// "unreserved" (about to exceed max_slot_wal_keep_size) or "lost"
	// (invalidated: the WAL it needs was removed). Empty when unknown.
	WALStatus string
}

// ReplicationSlotWarning is a reported slot or setting. Slot is empty for the
// max_slot_wal_keep_size warning.
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
       END,
       COALESCE(wal_status, '')
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
		if err := rows.Scan(&s.Name, &s.Active, &s.HeldBytes, &s.WALStatus); err != nil {
			return nil, maxKeep, fmt.Errorf("scan pg_replication_slots: %w", err)
		}
		slots = append(slots, s)
	}
	if err := rows.Err(); err != nil {
		return nil, maxKeep, fmt.Errorf("read pg_replication_slots: %w", err)
	}
	return slots, maxKeep, nil
}

// ReplicationSlotWarnings decides what to report at boot, after CDC started.
//
// CDC is the append-only trace of the SQL projection, so its own slot comes
// first:
//   - invalidated (wal_status "lost"): Postgres removed WAL the event log never
//     stored; the pipeline cannot resume without manual recovery;
//   - inactive: nothing is writing the event log;
//   - lagging: it holds more than lagBytes of WAL (thebe.cdc.max_lag_bytes;
//     0 = not checked), or wal_status is "unreserved" (about to hit the cap).
//
// Any other inactive slot is reported as stale: Postgres keeps every WAL
// segment after its restart_lsn until max_slot_wal_keep_size or the disk runs
// out. An unlimited max_slot_wal_keep_size is always reported.
func ReplicationSlotWarnings(slots []ReplicationSlot, maxKeep string, cdcEnabled bool, cdcSlot string, lagBytes int64) []ReplicationSlotWarning {
	var out []ReplicationSlotWarning
	for _, s := range slots {
		if cdcEnabled && s.Name == cdcSlot {
			if msg := cdcSlotProblem(s, lagBytes); msg != "" {
				out = append(out, ReplicationSlotWarning{Slot: s.Name, HeldBytes: s.HeldBytes, Message: msg})
			}
			continue
		}
		if s.Active {
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

// cdcSlotProblem describes what is wrong with the CDC slot, or "".
func cdcSlotProblem(s ReplicationSlot, lagBytes int64) string {
	switch {
	case s.WALStatus == "lost":
		return fmt.Sprintf("CDC slot %q is invalidated: Postgres removed WAL the event log never stored "+
			"(max_slot_wal_keep_size reached); the CDC pipeline cannot resume until the slot and event log are recovered", s.Name)
	case !s.Active:
		return fmt.Sprintf("CDC slot %q is inactive: nothing is writing the event log", s.Name)
	case s.WALStatus == "unreserved":
		return fmt.Sprintf("CDC slot %q is lagging and about to exceed max_slot_wal_keep_size (wal_status unreserved, %d bytes held)", s.Name, s.HeldBytes)
	case lagBytes > 0 && s.HeldBytes > lagBytes:
		return fmt.Sprintf("CDC slot %q is lagging: %d bytes of WAL held, over thebe.cdc.max_lag_bytes (%d)", s.Name, s.HeldBytes, lagBytes)
	}
	return ""
}
