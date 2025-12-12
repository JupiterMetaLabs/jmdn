package Cache

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)


func NewStatsBuilder(stats *Stats) *Stats {
	if stats != nil {
		return &Stats{
			mu: stats.mu,
			TotalPeers: stats.TotalPeers,
			ReachablePeers: stats.ReachablePeers,
			UnreachablePeers: stats.UnreachablePeers,
			TimeTaken: stats.TimeTaken,
		}
	}
	return &Stats{
		mu: &sync.RWMutex{},
		TotalPeers:       0,
		ReachablePeers:   make(map[peer.ID]multiaddr.Multiaddr),
		UnreachablePeers: make(map[peer.ID]multiaddr.Multiaddr),
	}
}

func (stats *Stats) AddReachablePeer(peerID peer.ID, addr multiaddr.Multiaddr) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	stats.ReachablePeers[peerID] = addr
}

func (stats *Stats) AddUnreachablePeer(peerID peer.ID, addr multiaddr.Multiaddr) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	stats.UnreachablePeers[peerID] = addr
}

func (stats *Stats) GetReachablePeers() map[peer.ID]multiaddr.Multiaddr {
	stats.mu.RLock()
	defer stats.mu.RUnlock()
	return stats.ReachablePeers
}

func (stats *Stats) GetUnreachablePeers() map[peer.ID]multiaddr.Multiaddr {
	stats.mu.RLock()
	defer stats.mu.RUnlock()
	return stats.UnreachablePeers
}

func (stats *Stats) GetTotalPeers() int {
	stats.mu.RLock()
	defer stats.mu.RUnlock()
	return stats.TotalPeers
}

func (stats *Stats) SetTimeTaken(timeTaken time.Duration) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	stats.TimeTaken = timeTaken
}

func (stats *Stats) GetTimeTaken() time.Duration {
	stats.mu.RLock()
	defer stats.mu.RUnlock()
	return stats.TimeTaken
}

func (stats *Stats) SetTotalPeers(totalPeers int) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	stats.TotalPeers = totalPeers
}