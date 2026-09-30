package DB_OPs

// Durable store for chain-anchored committee pools (W1,
// messaging/committee_anchor.go). One record per selection period: the
// verified, seed-signed snapshot that the anchor block of the PREVIOUS period
// carried. Same sync-KV discipline as the equivocation store
// (equivocation.go): a period's pool is fixed by the chain and must
// never be rewritten. Skips the write when a record already exists; callers
// must serialize, this is not atomic.

import (
	"fmt"
	"strconv"

	"gossipnode/config"
)

// CommitteeAnchorKey is the sync-KV key for one selection period's anchored pool.
func CommitteeAnchorKey(period uint64) string {
	return "committee_anchor:" + strconv.FormatUint(period, 10)
}

// GetCommitteeAnchor returns the raw record stored for period, found=false when
// none. conn may be nil.
func GetCommitteeAnchor(conn *config.PooledConnection, period uint64) (raw []byte, found bool, err error) {
	h, err := getHandle(conn)
	if err != nil {
		return nil, false, fmt.Errorf("committee anchor read: %w", err)
	}
	raw, err = h.GetSyncKV(CommitteeAnchorKey(period))
	if err != nil {
		if isNotFoundError(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("committee anchor read period %d: %w", period, err)
	}
	if raw == nil {
		return nil, false, nil
	}
	return raw, true, nil
}

// PutCommitteeAnchorIfAbsent stores raw for period unless a record already
// exists (skips write when record exists; callers must serialize, not atomic;
// a read error fails closed). conn may be nil.
func PutCommitteeAnchorIfAbsent(conn *config.PooledConnection, period uint64, raw []byte) error {
	h, err := getHandle(conn)
	if err != nil {
		return fmt.Errorf("committee anchor write: %w", err)
	}
	key := CommitteeAnchorKey(period)
	existing, gerr := h.GetSyncKV(key)
	if gerr != nil && !isNotFoundError(gerr) {
		return fmt.Errorf("committee anchor first-writer read (fail closed) period %d: %w", period, gerr)
	}
	if existing != nil {
		return nil
	}
	if err := h.PutSyncKV(key, raw); err != nil {
		return fmt.Errorf("committee anchor write period %d: %w", period, err)
	}
	return nil
}
