package PubSubMessages

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// StreamEntry represents a cached stream with metadata
type StreamEntry struct {
	Stream      network.Stream
	LastUsed    time.Time
	AccessCount int64
}

// StreamCache manages reusable streams using LRU with TTL
type StreamCache struct {
	Streams                map[peer.ID]*StreamEntry
	AccessOrder            []peer.ID     // LRU order (most recent at end)
	MaxStreams             int           // Maximum number of streams to cache
	TTL                    time.Duration // Time-to-live for idle streams
	Mutex                  sync.RWMutex
	Host                   host.Host
	ParallelCleanUpRoutine bool
}

func NewStreamCacheBuilder(streamcache *StreamCache) *StreamCache {
	if streamcache != nil {
		return &StreamCache{
			Streams:     streamcache.Streams,
			AccessOrder: streamcache.AccessOrder,
			MaxStreams:  streamcache.MaxStreams,
			TTL:         streamcache.TTL,
		}
	}
	return &StreamCache{}
}

func (streamcache *StreamCache) SetStreams(streams map[peer.ID]*StreamEntry) *StreamCache {
	streamcache.Streams = streams
	return streamcache
}

func (streamcache *StreamCache) SetAccessOrder(accessOrder []peer.ID) *StreamCache {
	streamcache.AccessOrder = accessOrder
	return streamcache
}

func (streamcache *StreamCache) SetMaxStreams(maxStreams int) *StreamCache {
	streamcache.MaxStreams = maxStreams
	return streamcache
}

func (streamcache *StreamCache) SetTTL(ttl time.Duration) *StreamCache {
	streamcache.TTL = ttl
	return streamcache
}

func (streamcache *StreamCache) SetHost(host host.Host) *StreamCache {
	streamcache.Host = host
	return streamcache
}

func (streamcache *StreamCache) SetParallelCleanUpRoutine(parallelCleanUpRoutine bool) *StreamCache {
	streamcache.ParallelCleanUpRoutine = parallelCleanUpRoutine
	return streamcache
}

func (streamcache *StreamCache) GetStreamCache() *StreamCache {
	return NewStreamCacheBuilder(streamcache)
}

func (streamcache *StreamCache) GetStreams() map[peer.ID]*StreamEntry {
	return streamcache.Streams
}

func (streamcache *StreamCache) GetAccessOrder() []peer.ID {
	return streamcache.AccessOrder
}

func (streamcache *StreamCache) GetMaxStreams() int {
	return streamcache.MaxStreams
}

func (streamcache *StreamCache) GetTTL() time.Duration {
	return streamcache.TTL
}

func (streamcache *StreamCache) GetHost() host.Host {
	return streamcache.Host
}

func (streamcache *StreamCache) GetParallelCleanUpRoutine() bool {
	return streamcache.ParallelCleanUpRoutine
}
