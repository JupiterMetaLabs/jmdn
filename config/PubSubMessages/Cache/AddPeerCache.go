package Cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	"gossipnode/config/PubSubMessages"
	"gossipnode/config/common"
	"gossipnode/node"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

var AddPeersCacheLocalGRO interfaces.LocalGoroutineManagerInterface

var (
	AddPeersCache   map[peer.ID]multiaddr.Multiaddr
	AddPeersCacheMu sync.Mutex // protect global cache
)

type Stats struct {
	mu               *sync.RWMutex
	TotalPeers       int
	ReachablePeers   map[peer.ID]multiaddr.Multiaddr
	UnreachablePeers map[peer.ID]multiaddr.Multiaddr
	TimeTaken        time.Duration
}

func NewAddPeersCache() map[peer.ID]multiaddr.Multiaddr { return make(map[peer.ID]multiaddr.Multiaddr) }

func AddPeer(peerID peer.ID, addr multiaddr.Multiaddr) {
	if AddPeersCache == nil {
		AddPeersCache = make(map[peer.ID]multiaddr.Multiaddr)
	}
	AddPeersCacheMu.Lock()
	AddPeersCache[peerID] = addr
	AddPeersCacheMu.Unlock()
}

func GetPeer(peerID peer.ID) multiaddr.Multiaddr {
	AddPeersCacheMu.Lock()
	defer AddPeersCacheMu.Unlock()
	if AddPeersCache == nil {
		return nil
	}
	return AddPeersCache[peerID]
}

func RemovePeer(peerID peer.ID) {
	AddPeersCacheMu.Lock()
	defer AddPeersCacheMu.Unlock()
	if AddPeersCache == nil {
		return
	}
	delete(AddPeersCache, peerID)
}

func GetAllPeers() map[peer.ID]multiaddr.Multiaddr {
	AddPeersCacheMu.Lock()
	defer AddPeersCacheMu.Unlock()
	if AddPeersCache == nil {
		return make(map[peer.ID]multiaddr.Multiaddr)
	}
	// return a copy to avoid external mutation races
	cp := make(map[peer.ID]multiaddr.Multiaddr, len(AddPeersCache))
	for k, v := range AddPeersCache {
		cp[k] = v
	}
	return cp
}

func GetPeerCount() int {
	AddPeersCacheMu.Lock()
	defer AddPeersCacheMu.Unlock()
	if AddPeersCache == nil {
		return 0
	}
	return len(AddPeersCache)
}

func ClearCache() {
	AddPeersCacheMu.Lock()
	AddPeersCache = make(map[peer.ID]multiaddr.Multiaddr)
	AddPeersCacheMu.Unlock()
}

// FallbackConnectionFunction: update cache only (no connect)
func FallbackConnectionFunction(peerID peer.ID, multiaddrs []string) {
	ctx := context.Background()
	if len(multiaddrs) == 0 {
		if l := cacheLogger(); l != nil {
			l.Debug(ctx, "No multiaddrs provided for fallback", ion.String("peer_id", peerID.String()))
		}
		return
	}

	reachableFound := false

	for _, multiaddrStr := range multiaddrs {
		addr, err := multiaddr.NewMultiaddr(multiaddrStr)
		if err != nil {
			if l := cacheLogger(); l != nil {
				l.Error(ctx, "Invalid multiaddr", err, ion.String("peer_id", peerID.String()))
			}
			continue
		}

		nm := GetNodeManager()
		if nm == nil {
			if l := cacheLogger(); l != nil {
				l.Warn(ctx, "NodeManager not available for reachability check", ion.String("peer_id", peerID.String()))
			}
			break
		}

		reachable, timeTaken, err := nm.PingMultiaddrWithRetries(multiaddrStr, 3)
		if err != nil {
			if l := cacheLogger(); l != nil {
				l.Error(ctx, "Error checking reachability", err, ion.String("peer_id", peerID.String()))
			}
			continue
		}
		if l := cacheLogger(); l != nil {
			l.Debug(ctx, "Reachability check completed", ion.String("peer_id", peerID.String()), ion.Duration("time_taken", timeTaken))
		}
		if !reachable {
			if l := cacheLogger(); l != nil {
				l.Debug(ctx, "Multiaddr not reachable", ion.String("peer_id", peerID.String()), ion.String("multiaddr", multiaddrStr))
			}
			continue
		}

		AddPeer(peerID, addr)
		reachableFound = true
		if l := cacheLogger(); l != nil {
			l.Debug(ctx, "Reachable fallback multiaddr found", ion.String("peer_id", peerID.String()), ion.String("multiaddr", multiaddrStr))
		}
		break
	}

	if !reachableFound {
		if l := cacheLogger(); l != nil {
			l.Warn(ctx, "No reachable fallback multiaddr found", ion.String("peer_id", peerID.String()))
		}
	}
}

// AddPeersTemporary: concurrent reachability check, adds all reachable peers
// Returns statistics about reachability checks and connections
func AddPeersTemporary(peers []PubSubMessages.Buddy_PeerMultiaddr) Stats {
	ctx := context.Background()
	if AddPeersCacheLocalGRO == nil {
		var err error
		AddPeersCacheLocalGRO, err = common.InitializeGRO(GRO.AddPeersCacheLocal)
		if err != nil {
			if l := cacheLogger(); l != nil {
				l.Error(ctx, "failed to initialize local gro", err)
			}
		}
	}

	startTime := time.Now()
	stats := NewStatsBuilder(nil)
	stats.SetTotalPeers(len(peers))

	AddPeersCacheMu.Lock()
	if AddPeersCache == nil {
		AddPeersCache = make(map[peer.ID]multiaddr.Multiaddr)
	}
	AddPeersCacheMu.Unlock()

	wg, err := AddPeersCacheLocalGRO.NewFunctionWaitGroup(ctx, GRO.AddPeersCacheWaitGroup)
	if err != nil {
		if l := cacheLogger(); l != nil {
			l.Error(ctx, "failed to create function wait group", err)
		}
		stats.TimeTaken = time.Since(startTime)
		return *stats
	}

	if l := cacheLogger(); l != nil {
		l.Info(ctx, "Starting concurrent reachability checks", ion.Int("peer_count", len(peers)))
	}

	nm := GetNodeManager()
	if nm == nil {
		if l := cacheLogger(); l != nil {
			l.Warn(ctx, "NodeManager not available")
		}
		stats.TimeTaken = time.Since(startTime)
		return *stats
	}

	for idx, buddy := range peers {
		AddPeersCacheLocalGRO.Go(GRO.AddPeersCacheThread, func(ctx context.Context) error {

			addrStr := buddy.Multiaddr.String()
			peerID := buddy.PeerID
			index := idx
			if l := cacheLogger(); l != nil {
				l.Debug(ctx, "Checking peer", ion.Int("thread", index), ion.String("peer_id", peerID.String()), ion.String("addr", addrStr))
			}

			if nm == nil {
				if l := cacheLogger(); l != nil {
					l.Warn(ctx, "NodeManager not available", ion.String("peer_id", peerID.String()))
				}
				stats.AddUnreachablePeer(peerID, buddy.Multiaddr)
				return fmt.Errorf("nodeManager not available")
			}

			reachable, timeTaken, err := nm.PingMultiaddrWithRetries(addrStr, 3)
			if err != nil {
				if l := cacheLogger(); l != nil {
					l.Error(ctx, "Reachability check error", err, ion.String("peer_id", peerID.String()))
				}
				stats.AddUnreachablePeer(peerID, buddy.Multiaddr)
				return fmt.Errorf("error: %v", err)
			}

			if l := cacheLogger(); l != nil {
				l.Debug(ctx, "Reachability check completed", ion.String("peer_id", peerID.String()), ion.Duration("time_taken", timeTaken), ion.Bool("reachable", reachable))
			}

			if reachable {
				stats.AddReachablePeer(peerID, buddy.Multiaddr)
				AddPeer(peerID, buddy.Multiaddr)
				if l := cacheLogger(); l != nil {
					l.Debug(ctx, "Peer added", ion.String("peer_id", peerID.String()))
				}
			} else {
				stats.AddUnreachablePeer(peerID, buddy.Multiaddr)
				return fmt.Errorf("peer %s not reachable", peerID)
			}
			return nil
		}, local.AddToWaitGroup(GRO.AddPeersCacheWaitGroup))
	}

	wg.Wait()

	reachablePeers := stats.GetReachablePeers()
	unreachablePeers := stats.GetUnreachablePeers()

	if l := cacheLogger(); l != nil {
		l.Info(ctx, "Reachability checks completed", ion.Int("reachable_count", len(reachablePeers)), ion.Int("unreachable_count", len(unreachablePeers)))
	}

	if len(reachablePeers) > 0 {
		if err := ConnectToTemporaryPeers(reachablePeers); err != nil {
			if l := cacheLogger(); l != nil {
				l.Error(ctx, "Connection failed", err)
			}
		} else {
			if l := cacheLogger(); l != nil {
				l.Debug(ctx, "Connected to peers", ion.Int("count", len(reachablePeers)))
			}
		}
	} else {
		if l := cacheLogger(); l != nil {
			l.Warn(ctx, "No reachable peers found")
		}
	}

	stats.TimeTaken = time.Since(startTime)
	return *stats
}

func GetNodeManager() *node.NodeManager {
	return node.GetNodeManagerInterface()
}

func ConnectToTemporaryPeers(peers map[peer.ID]multiaddr.Multiaddr) error {
	ctx := context.Background()
	nodeManager := GetNodeManager()
	if nodeManager == nil {
		return fmt.Errorf("NodeManager not available")
	}

	var connectedCount, failedCount int
	connectedPeers := make(map[peer.ID]bool)

	for peerID, addr := range peers {
		addrStr := addr.String()
		if l := cacheLogger(); l != nil {
			l.Debug(ctx, "Adding temporary peer for consensus", ion.String("peer_id", peerID.String()), ion.String("addr", addrStr))
		}

		if err := nodeManager.AddPeer(addrStr); err != nil {
			// Check if error is because peer is already connected (this is OK)
			if err.Error() == fmt.Sprintf("peer %s is already connected and managed", peerID) {
				if l := cacheLogger(); l != nil {
					l.Debug(ctx, "Peer already connected (OK)", ion.String("peer_id", peerID.String()))
				}
				connectedCount++
				connectedPeers[peerID] = true
			} else {
				if l := cacheLogger(); l != nil {
					l.Error(ctx, "Failed to add peer", err, ion.String("peer_id", peerID.String()))
				}
				failedCount++
			}
		} else {
			if l := cacheLogger(); l != nil {
				l.Debug(ctx, "Peer added to NodeManager for consensus", ion.String("peer_id", peerID.String()))
			}
			connectedCount++
			connectedPeers[peerID] = true
		}
	}

	// Verify actual libp2p connections (not just NodeManager tracking)
	// Get the host from NodeManager to check actual connections
	host := nodeManager.GetHost()
	if host == nil {
		return fmt.Errorf("cannot verify connections: host not available")
	}

	// Wait a moment for connections to establish
	time.Sleep(500 * time.Millisecond)

	// Verify each peer is actually connected
	actuallyConnected := 0
	for peerID := range connectedPeers {
		connectedness := host.Network().Connectedness(peerID)
		if connectedness == network.Connected {
			actuallyConnected++
			if l := cacheLogger(); l != nil {
				l.Debug(ctx, "Verified connection to peer", ion.String("peer_id", peerID.String()[:16]))
			}
		} else {
			if l := cacheLogger(); l != nil {
				l.Warn(ctx, "Peer not actually connected", ion.String("peer_id", peerID.String()[:16]), ion.String("connectedness", connectedness.String()))
			}
			// Remove from connectedPeers map
			delete(connectedPeers, peerID)
		}
	}

	if l := cacheLogger(); l != nil {
		l.Info(ctx, "Temporary peer connection summary",
			ion.Int("added_to_node_manager", connectedCount),
			ion.Int("failed", failedCount),
			ion.Int("actually_connected", actuallyConnected))
	}

	// Return error if we don't have enough actually connected peers
	if actuallyConnected < config.MaxMainPeers {
		return fmt.Errorf("insufficient connected peers: got %d actually connected, need exactly %d (MaxMainPeers). Added to NodeManager: %d, Failed: %d",
			actuallyConnected, config.MaxMainPeers, connectedCount, failedCount)
	}

	return nil
}

// DisconnectFromTemporaryPeers: disconnect from temporary peers - Should be called when consensus is done
func DisconnectFromTemporaryPeers(peers map[peer.ID]multiaddr.Multiaddr) error {
	nodeManager := GetNodeManager()
	if nodeManager == nil {
		return fmt.Errorf("NodeManager not available")
	}

	for peerID := range peers {
		nodeManager.RemovePeer(peerID.String())
	}
	return nil
}
