package NodeInfo

import (
	"sync"
	"time"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types/constants"
	localerrors "github.com/JupiterMetaLabs/JMDN-FastSync/common/types/errors"
	"github.com/libp2p/go-libp2p/core/peer"
)

type AUTHHandler struct {
	cache map[string]types.AUTHStructure
	mu    sync.RWMutex
}

var (
	once        sync.Once
	authHandler *AUTHHandler
)

// Time Complexity: O(1)
func (sync *sync_struct) AUTH() types.AUTHHandler {
	once.Do(func() {
		authHandler = &AUTHHandler{
			cache: make(map[string]types.AUTHStructure),
		}
	})
	return authHandler
}

// Time Complexity: O(1)
func (a *AUTHHandler) AddRecord(peerID peer.ID, UUID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := peerID.String()
	if record, ok := a.cache[key]; ok && record.UUID == UUID && time.Now().Before(record.TTL) {
		record.TTL = time.Now().Add(constants.AUTH_TTL)
		a.cache[key] = record
	} else {
		a.cache[key] = types.AUTHStructure{
			UUID: UUID,
			TTL:  time.Now().Add(constants.AUTH_TTL),
		}
	}
	return nil
}

// Time Complexity: O(1)
func (a *AUTHHandler) RemoveRecord(peerID peer.ID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.cache, peerID.String())
	return nil
}

// Time Complexity: O(1)
func (a *AUTHHandler) GetRecord(peerID peer.ID) (types.AUTHStructure, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := peerID.String()
	record, ok := a.cache[key]
	if !ok {
		return types.AUTHStructure{}, localerrors.RecordNotFound
	}
	if time.Now().After(record.TTL) {
		delete(a.cache, key)
		return types.AUTHStructure{}, localerrors.RecordExpired
	}
	return record, nil
}

// Time Complexity: O(1)
func (a *AUTHHandler) IsAUTH(peerID peer.ID, UUID string) (bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	record, ok := a.cache[peerID.String()]
	if !ok {
		return false, localerrors.RecordNotFound
	}
	if record.UUID != UUID {
		return false, localerrors.RecordNotFound
	}
	return time.Now().Before(record.TTL), nil
}

// Time Complexity: O(1)
func (a *AUTHHandler) ResetTTL(peerID peer.ID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if record, ok := a.cache[peerID.String()]; ok {
		record.TTL = time.Now().Add(constants.AUTH_TTL)
		a.cache[peerID.String()] = record
		return nil
	}
	return localerrors.RecordNotFound
}
