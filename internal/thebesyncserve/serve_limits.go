package thebesyncserve

// Serve-side limits for GetBlocks. Every request makes the server read up to
// MaxBlocksPerRequest full blocks from its store and encode them, so the
// handler bounds how often one peer can ask, how many serves run at once (per
// peer and in total), how many bytes one response may carry, and how long a
// peer may take to send its request or drain the response.

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/time/rate"
)

const (
	// Per-peer request rate. A syncing peer issues batches back to back; a
	// request over the rate waits (up to serveWaitMax) rather than failing, so
	// honest catch-up slows down instead of aborting.
	getBlocksPerPeerRate  = rate.Limit(4)
	getBlocksPerPeerBurst = 8
	// getBlocksPerPeerInFlight bounds serves in progress for one peer, so a
	// single peer ID cannot occupy the whole global pool.
	getBlocksPerPeerInFlight = 2
	// getBlocksMaxConcurrent bounds serves in progress across all peers.
	getBlocksMaxConcurrent = 8
	// serveWaitMax is the longest a request waits for a rate token or a serve
	// slot before it is refused with errBusy.
	serveWaitMax = 5 * time.Second
	// maxTrackedPeers bounds the per-peer limiter table.
	maxTrackedPeers = 1024
)

const errBusy = "busy"

// Stream deadlines. A serve slot is held while the response is written, and a
// libp2p stream write blocks once the muxer window fills, so a peer that never
// reads would otherwise hold its slot forever. These are vars so tests can
// shorten them.
var (
	// serveReadTimeout bounds how long the server waits for the request line.
	serveReadTimeout = 10 * time.Second
	// serveWriteBaseTimeout plus len(response)/serveWriteMinRate bounds the
	// response write. A peer that drains slower than serveWriteMinRate has its
	// stream reset and must retry with a smaller range.
	serveWriteBaseTimeout = 10 * time.Second
	serveWriteMinRate     = int64(1 << 20) // bytes per second
)

// serveWriteTimeout returns the write deadline budget for a response of n bytes.
func serveWriteTimeout(n int) time.Duration {
	return serveWriteBaseTimeout + time.Duration(int64(n)/serveWriteMinRate)*time.Second
}

// serveByteBudget is the most raw block bytes one GetBlocks response carries.
// Blocks are base64 in JSON (4/3 inflation), so this keeps the encoded response
// under maxGetBlocksRespBytes with headroom. At least one block is always
// returned so a single large block still makes progress.
func serveByteBudget() int64 { return maxGetBlocksRespBytes / 2 }

type serveLimits struct {
	once  sync.Once
	slots chan struct{}

	mu    sync.Mutex
	peers map[peer.ID]*peerLimiter
}

type peerLimiter struct {
	lim      *rate.Limiter
	inFlight chan struct{}
	lastSeen time.Time
}

func (l *serveLimits) init() {
	l.once.Do(func() {
		l.slots = make(chan struct{}, getBlocksMaxConcurrent)
		l.peers = make(map[peer.ID]*peerLimiter)
	})
}

// limiterFor returns p's limiter, creating it if needed. It returns nil only
// when the table is full and every tracked peer has a serve in flight.
func (l *serveLimits) limiterFor(p peer.ID) *peerLimiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if pl, ok := l.peers[p]; ok {
		pl.lastSeen = now
		return pl
	}
	if len(l.peers) >= maxTrackedPeers {
		// Drop the least recently seen entry that has no serve in flight.
		// Linear, but only on insert at capacity, and the table is small.
		var oldest peer.ID
		var oldestAt time.Time
		for id, pl := range l.peers {
			if len(pl.inFlight) > 0 {
				continue
			}
			if oldestAt.IsZero() || pl.lastSeen.Before(oldestAt) {
				oldest, oldestAt = id, pl.lastSeen
			}
		}
		if oldestAt.IsZero() {
			return nil
		}
		delete(l.peers, oldest)
	}
	pl := &peerLimiter{
		lim:      rate.NewLimiter(getBlocksPerPeerRate, getBlocksPerPeerBurst),
		inFlight: make(chan struct{}, getBlocksPerPeerInFlight),
		lastSeen: now,
	}
	l.peers[p] = pl
	return pl
}

// acquire waits for p's rate token, a per-peer in-flight slot and a global
// serve slot, up to serveWaitMax in total. On success the caller must call the
// returned release.
func (l *serveLimits) acquire(p peer.ID) (release func(), ok bool) {
	l.init()
	pl := l.limiterFor(p)
	if pl == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), serveWaitMax)
	defer cancel()
	if err := pl.lim.Wait(ctx); err != nil {
		return nil, false
	}
	select {
	case pl.inFlight <- struct{}{}:
	case <-ctx.Done():
		return nil, false
	}
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots; <-pl.inFlight }, true
	case <-ctx.Done():
		<-pl.inFlight
		return nil, false
	}
}

// writeResponse writes one frame under a size-derived write deadline. On any
// write failure the stream is reset so the peer sees an abort rather than a
// truncated frame.
func writeResponse(stream network.Stream, b []byte) error {
	_ = stream.SetWriteDeadline(time.Now().Add(serveWriteTimeout(len(b))))
	if err := writeFrame(stream, b); err != nil {
		_ = stream.Reset()
		return err
	}
	return nil
}
