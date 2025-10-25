// =============================================================================
// FILE: pkg/bft/public_keys.go
// =============================================================================
package bft

import "sync"

var (
	pkMu sync.RWMutex
	// simple map: buddyID -> public key bytes
	publicKeyRegistry = map[string][]byte{}
)

func RegisterPublicKey(buddyID string, pub []byte) {
	pkMu.Lock()
	publicKeyRegistry[buddyID] = pub
	pkMu.Unlock()
}

func GetPublicKey(buddyID string) ([]byte, bool) {
	pkMu.RLock()
	defer pkMu.RUnlock()
	p, ok := publicKeyRegistry[buddyID]
	return p, ok
}
