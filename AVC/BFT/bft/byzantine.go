// =============================================================================
// FILE: pkg/bft/byzantine.go
// =============================================================================
package bft

import (
	"context"
	"sync"
	"time"
	"gossipnode/config/GRO"
)

type byzantineEntry struct {
	bannedAt time.Time
	ttl      time.Duration
}

type byzantineDetector struct {
	mu         sync.RWMutex
	byzantine  map[string]byzantineEntry
	defaultTTL time.Duration
}

func newByzantineDetector() *byzantineDetector {
	b := &byzantineDetector{
		byzantine:  make(map[string]byzantineEntry),
		defaultTTL: 5 * time.Minute,
	}
	BFTLocal.Go(GRO.BFTByzantineDetectorThread, func(ctx context.Context) error {
		b.cleaner()
		return nil
	})
	return b
}

func (b *byzantineDetector) detectConflicts(
	commit *CommitMessage,
	ourPrepares map[string]*PrepareMessage,
) []string {
	conflicts := make([]string, 0)

	for _, proofPrepare := range commit.PrepareProof {
		ourPrepare, exists := ourPrepares[proofPrepare.BuddyID]
		if !exists {
			continue
		}

		if ourPrepare.Decision != proofPrepare.Decision {
			conflicts = append(conflicts, proofPrepare.BuddyID)
			continue
		}

		if ourPrepare.Round != proofPrepare.Round ||
			ourPrepare.BlockHash != proofPrepare.BlockHash {
			conflicts = append(conflicts, proofPrepare.BuddyID)
		}
	}

	return conflicts
}

func (b *byzantineDetector) mark(buddyID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.byzantine[buddyID] = byzantineEntry{
		bannedAt: time.Now().UTC(),
		ttl:      b.defaultTTL,
	}
}

func (b *byzantineDetector) isByzantine(buddyID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	entry, ok := b.byzantine[buddyID]
	if !ok {
		return false
	}
	return time.Since(entry.bannedAt) <= entry.ttl
}

func (b *byzantineDetector) count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	now := time.Now().UTC()
	for _, entry := range b.byzantine {
		if now.Sub(entry.bannedAt) <= entry.ttl {
			n++
		}
	}
	return n
}

func (b *byzantineDetector) getAll() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	now := time.Now().UTC()
	ids := make([]string, 0, len(b.byzantine))
	for id, entry := range b.byzantine {
		if now.Sub(entry.bannedAt) <= entry.ttl {
			ids = append(ids, id)
		}
	}
	return ids
}

func (b *byzantineDetector) cleaner() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		b.mu.Lock()
		now := time.Now().UTC()
		for id, entry := range b.byzantine {
			if now.Sub(entry.bannedAt) > entry.ttl {
				delete(b.byzantine, id)
			}
		}
		b.mu.Unlock()
	}
}
