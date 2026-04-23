package MessagePassing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gossipnode/AVC/BuddyNodes/common"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Global variables for singleton cleanup routine
var (
	// globalStreamCaches tracks all registered StreamCache instances
	globalStreamCaches      []*AVCStruct.StreamCache
	globalStreamCachesMutex sync.RWMutex
	// cleanupOnce ensures only one cleanup goroutine is spawned
	cleanupOnce sync.Once
	// cleanupRunning indicates if the cleanup routine is active
	// cleanupRunning bool  // unused: assigned but never read
)

// <-- Use Builder Pattern Here -->
type StructStreamCache struct {
	StreamCache *AVCStruct.StreamCache
}

func NewStreamCacheBuilder(streamcache *AVCStruct.StreamCache) *StructStreamCache {
	if streamcache == nil {
		return &StructStreamCache{
			StreamCache: &AVCStruct.StreamCache{
				Streams:                make(map[peer.ID]*AVCStruct.StreamEntry),
				ParallelCleanUpRoutine: false,
			},
		}
	}
	return &StructStreamCache{
		StreamCache: streamcache,
	}
}

func (sc *StructStreamCache) SetAccessOrder() *StructStreamCache {
	sc.StreamCache.AccessOrder = make([]peer.ID, 0, sc.StreamCache.MaxStreams)
	return sc
}

func (sc *StructStreamCache) SetTTL(ttl time.Duration) *StructStreamCache {
	sc.StreamCache.TTL = ttl
	return sc
}

func (sc *StructStreamCache) SetHost(host host.Host) *StructStreamCache {
	sc.StreamCache.Host = host
	return sc
}

func (sc *StructStreamCache) SetMaxStreams(maxstreams int) *StructStreamCache {
	sc.StreamCache.MaxStreams = maxstreams
	return sc
}

func (sc *StructStreamCache) GetStreamCache() *AVCStruct.StreamCache {
	return sc.StreamCache
}

func (sc *StructStreamCache) Build() (*StructStreamCache, error) {
	if sc.StreamCache.Host == nil {
		return nil, fmt.Errorf("host is not set")
	}
	if sc.StreamCache.MaxStreams == 0 {
		return nil, fmt.Errorf("max streams is not set")
	}
	if sc.StreamCache.TTL == 0 {
		return nil, fmt.Errorf("ttl is not set")
	}
	if sc.StreamCache.AccessOrder == nil {
		return nil, fmt.Errorf("access order is not set")
	}
	return sc, nil
}

// GetSubmitMessageStream gets or creates a stream using SubmitMessageProtocol
// This is specifically for subscription requests and vote submissions
// Each call creates a fresh stream to ensure clean state for request/response patterns
func (sc *StructStreamCache) GetSubmitMessageStream(ctx context.Context, peerID peer.ID) (network.Stream, error) {
	// CRITICAL FIX: Do NOT hold lock during NewStream.
	// NewStream involves network negotiation which can take seconds if the network is congested or peer is slow.
	// Holding the lock here would serialize all concurrent subscription requests, leading to timeouts (sum of delays > 10s).
	// By moving it outside, we allow parallel stream creation.

	// Create new stream using SubmitMessageProtocol using provided context
	// We do not cache this stream as it is used for one-off request/response patterns
	return sc.StreamCache.Host.NewStream(ctx, peerID, config.SubmitMessageProtocol)
}

// GetStream gets or creates a stream to the specified peer
func (sc *StructStreamCache) GetStream(peerID peer.ID) (network.Stream, error) {
	// First check cache with lock
	sc.StreamCache.Mutex.Lock()
	if entry, exists := sc.StreamCache.Streams[peerID]; exists {
		// Check if stream is still valid
		if entry.Stream.Conn().Stat().Direction != network.DirUnknown {
			// Update access time and move to end of LRU list
			entry.LastUsed = time.Now().UTC()
			entry.AccessCount++
			sc.moveToEnd(peerID)
			sc.StreamCache.Mutex.Unlock() // Unlock before returning
			return entry.Stream, nil
		}
		// Stream is invalid, remove it
		sc.removeEntry(peerID)
	}
	sc.StreamCache.Mutex.Unlock() // Unlock before network call

	// Create new stream - slow network operation outside lock
	stream, err := sc.StreamCache.Host.NewStream(context.Background(), peerID, config.BuddyNodesMessageProtocol)
	if err != nil {
		return nil, err
	}

	// Re-acquire lock to update cache
	sc.StreamCache.Mutex.Lock()
	defer sc.StreamCache.Mutex.Unlock()

	// Add to cache
	sc.addEntry(peerID, stream)
	return stream, nil
}

// addEntry adds a new stream entry to the cache
func (sc *StructStreamCache) addEntry(peerID peer.ID, stream network.Stream) {
	// If cache is full, remove least recently used
	if len(sc.StreamCache.Streams) >= sc.StreamCache.MaxStreams {
		sc.evictLRU()
	}

	entry := &AVCStruct.StreamEntry{
		Stream:      stream,
		LastUsed:    time.Now().UTC(),
		AccessCount: 1,
	}

	sc.StreamCache.Streams[peerID] = entry
	sc.StreamCache.AccessOrder = append(sc.StreamCache.AccessOrder, peerID)
}

// removeEntry removes a stream entry from the cache
func (sc *StructStreamCache) removeEntry(peerID peer.ID) {
	if entry, exists := sc.StreamCache.Streams[peerID]; exists {
		entry.Stream.Close()
		delete(sc.StreamCache.Streams, peerID)
		sc.removeFromOrder(peerID)
	}
}

// moveToEnd moves a peer to the end of the access order (most recent)
func (sc *StructStreamCache) moveToEnd(peerID peer.ID) {
	sc.removeFromOrder(peerID)
	sc.StreamCache.AccessOrder = append(sc.StreamCache.AccessOrder, peerID)
}

// removeFromOrder removes a peer from the access order
func (sc *StructStreamCache) removeFromOrder(peerID peer.ID) {
	for i, id := range sc.StreamCache.AccessOrder {
		if id == peerID {
			sc.StreamCache.AccessOrder = append(sc.StreamCache.AccessOrder[:i], sc.StreamCache.AccessOrder[i+1:]...)
			break
		}
	}
}

// evictLRU removes the least recently used stream
func (sc *StructStreamCache) evictLRU() {
	if len(sc.StreamCache.AccessOrder) == 0 {
		return
	}

	// Remove the first (oldest) entry
	oldestPeerID := sc.StreamCache.AccessOrder[0]
	sc.removeEntry(oldestPeerID)
}

// CloseStream closes and removes a stream from the cache
func (sc *StructStreamCache) CloseStream(peerID peer.ID) {
	sc.StreamCache.Mutex.Lock()
	defer sc.StreamCache.Mutex.Unlock()
	sc.removeEntry(peerID)
}

// CloseAll closes all streams in the cache and unregisters from global cleanup
func (sc *StructStreamCache) CloseAll() {
	sc.StreamCache.Mutex.Lock()
	defer sc.StreamCache.Mutex.Unlock()

	for peerID := range sc.StreamCache.Streams {
		sc.removeEntry(peerID)
	}

	// Unregister from global cleanup routine to prevent memory leak
	sc.unregisterStreamCache()
}

// CleanupExpiredStreams removes streams that have exceeded their TTL
func (sc *StructStreamCache) CleanupExpiredStreams() {
	sc.StreamCache.Mutex.Lock()
	defer sc.StreamCache.Mutex.Unlock()

	now := time.Now().UTC()
	expiredPeers := make([]peer.ID, 0)

	for peerID, entry := range sc.StreamCache.Streams {
		// Check if stream is invalid or expired
		if entry.Stream.Conn().Stat().Direction == network.DirUnknown ||
			now.Sub(entry.LastUsed) > sc.StreamCache.TTL {
			expiredPeers = append(expiredPeers, peerID)
		}
	}

	// Remove expired streams
	for _, peerID := range expiredPeers {
		sc.removeEntry(peerID)
	}
}

// GetStats returns cache statistics
func (sc *StructStreamCache) GetStats() map[string]interface{} {
	sc.StreamCache.Mutex.RLock()
	defer sc.StreamCache.Mutex.RUnlock()

	totalAccesses := int64(0)
	for _, entry := range sc.StreamCache.Streams {
		totalAccesses += entry.AccessCount
	}

	return map[string]interface{}{
		"active_streams": len(sc.StreamCache.Streams),
		"max_streams":    sc.StreamCache.MaxStreams,
		"ttl_seconds":    sc.StreamCache.TTL.Seconds(),
		"total_accesses": totalAccesses,
	}
}

// registerStreamCache adds a StreamCache instance to the global registry
func (sc *StructStreamCache) registerStreamCache() {
	globalStreamCachesMutex.Lock()
	defer globalStreamCachesMutex.Unlock()

	// Check if already registered
	for _, cache := range globalStreamCaches {
		if cache == sc.StreamCache {
			return
		}
	}

	globalStreamCaches = append(globalStreamCaches, sc.StreamCache)
}

// unregisterStreamCache removes a StreamCache instance from the global registry
func (sc *StructStreamCache) unregisterStreamCache() {
	globalStreamCachesMutex.Lock()
	defer globalStreamCachesMutex.Unlock()

	for i, cache := range globalStreamCaches {
		if cache == sc.StreamCache {
			// Create a new slice to avoid issues with underlying array
			newCaches := make([]*AVCStruct.StreamCache, 0, len(globalStreamCaches)-1)
			newCaches = append(newCaches, globalStreamCaches[:i]...)
			newCaches = append(newCaches, globalStreamCaches[i+1:]...)
			globalStreamCaches = newCaches
			return
		}
	}
}

// ParallelCleanUpRoutine starts a single global cleanup routine for all StreamCache instances
// This ensures only ONE goroutine runs regardless of how many StreamCache instances exist
func (sc *StructStreamCache) ParallelCleanUpRoutine() {
	if ListenerHandlerLocal == nil {
		var err error
		ListenerHandlerLocal, err = common.InitializeGRO(GRO.HandleBFTRequestLocal)
		if err != nil {
			fmt.Printf("❌ Failed to initialize ListenerHandler local manager: %v\n", err)
			return
		}
	}

	// Register this StreamCache instance
	sc.registerStreamCache()

	// Use sync.Once to ensure only one cleanup goroutine is spawned globally
	cleanupOnce.Do(func() {
		// cleanupRunning = true  // unused: assigned but never read
		fmt.Println("🚀 Starting global StreamCache cleanup routine (singleton)")

		ListenerHandlerLocal.Go(GRO.StreamCacheParallelCleanUpRoutineThread, func(ctx context.Context) error {
			defer func() {
				// cleanupRunning = false  // unused: assigned but never read
			}()

			for {
				select {
				case <-ctx.Done():
					fmt.Println("🛑 Global StreamCache cleanup routine stopping")
					return nil
				default:
					// Cleanup all registered StreamCache instances
					globalStreamCachesMutex.RLock()
					caches := make([]*AVCStruct.StreamCache, len(globalStreamCaches))
					copy(caches, globalStreamCaches)
					globalStreamCachesMutex.RUnlock()

					for _, cache := range caches {
						if cache != nil {
							// Create a temporary StructStreamCache to call CleanupExpiredStreams
							tempSC := &StructStreamCache{StreamCache: cache}
							tempSC.CleanupExpiredStreams()
						}
					}

					time.Sleep(5 * time.Second)
				}
			}
		})
	})
}
