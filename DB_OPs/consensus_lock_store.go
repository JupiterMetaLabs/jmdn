package DB_OPs

import "fmt"

// ConsensusLockStore adapts the node's sync-state KV to roundlock.Store, so
// per-round signing records survive restarts. Keys are namespaced by the
// ledger's prefix.
type ConsensusLockStore struct{}

// Get returns the stored value for key, or (nil, nil) when absent.
func (ConsensusLockStore) Get(key string) ([]byte, error) {
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("consensus lock %s: %w", key, err)
	}
	return h.GetSyncKV(key)
}

// Put stores value under key.
func (ConsensusLockStore) Put(key string, value []byte) error {
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("consensus lock %s: %w", key, err)
	}
	return h.PutSyncKV(key, value)
}
