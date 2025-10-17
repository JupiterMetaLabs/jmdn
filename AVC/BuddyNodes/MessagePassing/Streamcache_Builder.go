package MessagePassing

import (
	"context"
	"fmt"
	"gossipnode/config"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// StreamEntry represents a cached stream with metadata
type StreamEntry struct {
	stream      network.Stream
	lastUsed    time.Time
	accessCount int64
}

// StreamCache manages reusable streams using LRU with TTL
type StreamCache struct {
	streams     map[peer.ID]*StreamEntry
	accessOrder []peer.ID     // LRU order (most recent at end)
	maxStreams  int           // Maximum number of streams to cache
	ttl         time.Duration // Time-to-live for idle streams
	mutex       sync.RWMutex
	host        host.Host
}


// <-- Use Builder Pattern Here -->
func NewStreamCacheBuilder() *StreamCache {
	return &StreamCache{
		streams: make(map[peer.ID]*StreamEntry),
	}
}

func (sc *StreamCache) SetAccessOrder() *StreamCache {
	sc.accessOrder = make([]peer.ID, 0, sc.maxStreams)
	return sc
}

func (sc *StreamCache) SetTTL(ttl time.Duration) *StreamCache {
	sc.ttl = ttl
	return sc
}

func (sc *StreamCache) SetHost(host host.Host) *StreamCache {
	sc.host = host
	return sc
}

func (sc *StreamCache) SetMaxStreams(maxstreams int) *StreamCache {
	sc.maxStreams = maxstreams
	return sc
}

func (sc *StreamCache) Build() (*StreamCache, error) {
	if sc.host == nil {
		return nil, fmt.Errorf("host is not set")
	}
	if sc.maxStreams == 0 {
		return nil, fmt.Errorf("max streams is not set")
	}
	if sc.ttl == 0 {
		return nil, fmt.Errorf("ttl is not set")
	}
	if sc.accessOrder == nil {
		return nil, fmt.Errorf("access order is not set")
	}
	return sc, nil
}

// GetStream gets or creates a stream to the specified peer
func (sc *StreamCache) GetStream(peerID peer.ID) (network.Stream, error) {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()

	// Check if we already have a valid stream
	if entry, exists := sc.streams[peerID]; exists {
		// Check if stream is still valid
		if entry.stream.Conn().Stat().Direction != network.DirUnknown {
			// Update access time and move to end of LRU list
			entry.lastUsed = time.Now()
			entry.accessCount++
			sc.moveToEnd(peerID)
			return entry.stream, nil
		}
		// Stream is invalid, remove it
		sc.removeEntry(peerID)
	}

	// Create new stream
	stream, err := sc.host.NewStream(context.Background(), peerID, config.BuddyNodesMessageProtocol)
	if err != nil {
		return nil, err
	}

	// Add to cache
	sc.addEntry(peerID, stream)
	return stream, nil
}

// addEntry adds a new stream entry to the cache
func (sc *StreamCache) addEntry(peerID peer.ID, stream network.Stream) {
	// If cache is full, remove least recently used
	if len(sc.streams) >= sc.maxStreams {
		sc.evictLRU()
	}

	entry := &StreamEntry{
		stream:      stream,
		lastUsed:    time.Now(),
		accessCount: 1,
	}

	sc.streams[peerID] = entry
	sc.accessOrder = append(sc.accessOrder, peerID)
}

// removeEntry removes a stream entry from the cache
func (sc *StreamCache) removeEntry(peerID peer.ID) {
	if entry, exists := sc.streams[peerID]; exists {
		entry.stream.Close()
		delete(sc.streams, peerID)
		sc.removeFromOrder(peerID)
	}
}

// moveToEnd moves a peer to the end of the access order (most recent)
func (sc *StreamCache) moveToEnd(peerID peer.ID) {
	sc.removeFromOrder(peerID)
	sc.accessOrder = append(sc.accessOrder, peerID)
}

// removeFromOrder removes a peer from the access order
func (sc *StreamCache) removeFromOrder(peerID peer.ID) {
	for i, id := range sc.accessOrder {
		if id == peerID {
			sc.accessOrder = append(sc.accessOrder[:i], sc.accessOrder[i+1:]...)
			break
		}
	}
}

// evictLRU removes the least recently used stream
func (sc *StreamCache) evictLRU() {
	if len(sc.accessOrder) == 0 {
		return
	}

	// Remove the first (oldest) entry
	oldestPeerID := sc.accessOrder[0]
	sc.removeEntry(oldestPeerID)
}

// CloseStream closes and removes a stream from the cache
func (sc *StreamCache) CloseStream(peerID peer.ID) {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()
	sc.removeEntry(peerID)
}

// CloseAll closes all streams in the cache
func (sc *StreamCache) CloseAll() {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()

	for peerID := range sc.streams {
		sc.removeEntry(peerID)
	}
}

// CleanupExpiredStreams removes streams that have exceeded their TTL
func (sc *StreamCache) CleanupExpiredStreams() {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()

	now := time.Now()
	expiredPeers := make([]peer.ID, 0)

	for peerID, entry := range sc.streams {
		// Check if stream is invalid or expired
		if entry.stream.Conn().Stat().Direction == network.DirUnknown ||
			now.Sub(entry.lastUsed) > sc.ttl {
			expiredPeers = append(expiredPeers, peerID)
		}
	}

	// Remove expired streams
	for _, peerID := range expiredPeers {
		sc.removeEntry(peerID)
	}
}

// GetStats returns cache statistics
func (sc *StreamCache) GetStats() map[string]interface{} {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()

	totalAccesses := int64(0)
	for _, entry := range sc.streams {
		totalAccesses += entry.accessCount
	}

	return map[string]interface{}{
		"active_streams": len(sc.streams),
		"max_streams":    sc.maxStreams,
		"ttl_seconds":    sc.ttl.Seconds(),
		"total_accesses": totalAccesses,
	}
}