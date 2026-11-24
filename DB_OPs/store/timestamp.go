// MODULE: DB_OPs/store/timestamp.go
// PURPOSE: Unit-normalize account UpdatedAt/CreatedAt values before they reach
//          a TIMESTAMPTZ column, so LWW comparisons stay unit-safe.
//
// WHY: stored account timestamps are MIXED-UNIT — the live executor stamps
// UpdatedAt in Unix SECONDS (block timestamp) while sync/recon paths stamp
// nanoseconds (time.Now().UnixNano()). If a seconds value is fed to
// time.Unix(0, x) it is interpreted as nanoseconds, landing ~9 orders of
// magnitude in the past, and the projector's LWW guard
// (WHERE accounts.updated_at < EXCLUDED.updated_at) then lets a stale sync
// write beat a newer live write (RCA §3a inversion).
//
// SYNC OBLIGATION: this MUST stay behaviourally identical to
// DB_OPs.normalizeUpdatedAtNanos (merge_account.go), which is pinned
// byte-for-byte by merge_account_test.go. The two live in different packages
// only to avoid an import cycle; change them together.

package store

import "time"

// NormalizeUpdatedAtNanos converts an epoch value of unknown unit
// (seconds, millis, micros, or nanos) to nanoseconds.
func NormalizeUpdatedAtNanos(ts int64) int64 {
	switch {
	case ts <= 0:
		return ts
	case ts < 1e11: // seconds (valid until year ~5138)
		return ts * int64(time.Second)
	case ts < 1e14: // milliseconds
		return ts * int64(time.Millisecond)
	case ts < 1e17: // microseconds
		return ts * int64(time.Microsecond)
	default: // already nanoseconds
		return ts
	}
}

// NormalizedUnixTime returns a time.Time from a mixed-unit epoch int64,
// normalizing to nanoseconds first. Zero/negative inputs map to the zero time.
func NormalizedUnixTime(ts int64) time.Time {
	if ts <= 0 {
		return time.Time{}
	}
	return time.Unix(0, NormalizeUpdatedAtNanos(ts)).UTC()
}
