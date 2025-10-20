package Structs

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
	Streams     map[peer.ID]*StreamEntry
	AccessOrder []peer.ID     // LRU order (most recent at end)
	MaxStreams  int           // Maximum number of streams to cache
	TTL        time.Duration // Time-to-live for idle streams
	Mutex       sync.RWMutex
	Host        host.Host
	ParallelCleanUpRoutine bool
}