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
	if len(multiaddrs) == 0 {
		fmt.Printf("[%s] No multiaddrs provided for fallback\n", peerID)
		return
	}

	reachableFound := false

	for _, multiaddrStr := range multiaddrs {
		addr, err := multiaddr.NewMultiaddr(multiaddrStr)
		if err != nil {
			fmt.Printf("[%s] Invalid multiaddr: %v\n", peerID, err)
			continue
		}

		nm := GetNodeManager()
		if nm == nil {
			fmt.Printf("[%s] NodeManager not available for reachability check\n", peerID)
			break
		}

		reachable, timeTaken, err := nm.PingMultiaddrWithRetries(multiaddrStr, 3)
		if err != nil {
			fmt.Printf("[%s] Error checking reachability: %v\n", peerID, err)
			continue
		}
		fmt.Printf("[%s] Time taken to check reachability: %v\n", peerID, timeTaken)
		if !reachable {
			fmt.Printf("[%s] Multiaddr not reachable: %s\n", peerID, multiaddrStr)
			continue
		}

		AddPeer(peerID, addr)
		reachableFound = true
		fmt.Printf("[%s] Reachable fallback multiaddr found: %s\n", peerID, multiaddrStr)
		break
	}

	if !reachableFound {
		fmt.Printf("[%s] No reachable fallback multiaddr found\n", peerID)
	}
}

// AddPeersTemporary: concurrent reachability check, adds all reachable peers
// Returns statistics about reachability checks and connections
func AddPeersTemporary(peers []PubSubMessages.Buddy_PeerMultiaddr) Stats {
	if AddPeersCacheLocalGRO == nil {
		var err error
		AddPeersCacheLocalGRO, err = common.InitializeGRO(GRO.AddPeersCacheLocal)
		if err != nil {
			fmt.Printf("failed to initialize local gro: %v", err)
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

	wg, err := AddPeersCacheLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.AddPeersCacheWaitGroup)
	if err != nil {
		fmt.Printf("failed to create function wait group: %v", err)
		stats.TimeTaken = time.Since(startTime)
		return *stats
	}

	fmt.Println("---- Starting concurrent reachability checks ----")

	nm := GetNodeManager()
	if nm == nil {
		fmt.Println("NodeManager not available")
		stats.TimeTaken = time.Since(startTime)
		return *stats
	}

	for idx, buddy := range peers {
		AddPeersCacheLocalGRO.Go(GRO.AddPeersCacheThread, func(ctx context.Context) error {

			addrStr := buddy.Multiaddr.String()
			peerID := buddy.PeerID
			index := idx
			fmt.Printf("[Thread %d] Checking peer %s at %s\n", index, peerID, addrStr)

			if nm == nil {
				fmt.Printf("[%s] NodeManager not available\n", peerID)
				stats.AddUnreachablePeer(peerID, buddy.Multiaddr)
				return fmt.Errorf("nodeManager not available")
			}

			reachable, timeTaken, err := nm.PingMultiaddrWithRetries(addrStr, 3)
			if err != nil {
				fmt.Printf("[%s] Error: %v\n", peerID, err)
				stats.AddUnreachablePeer(peerID, buddy.Multiaddr)
				return fmt.Errorf("error: %v", err)
			}

			fmt.Printf("[%s] Time: %v, Reachable: %v\n", peerID, timeTaken, reachable)

			if reachable {
				stats.AddReachablePeer(peerID, buddy.Multiaddr)
				AddPeer(peerID, buddy.Multiaddr)
				fmt.Printf("✓ Peer %s added\n", peerID)
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

	fmt.Printf("\n---- Found %d reachable peers (unreachable: %d) ----\n",
		len(reachablePeers), len(unreachablePeers))

	if len(reachablePeers) > 0 {
		if err := ConnectToTemporaryPeers(reachablePeers); err != nil {
			fmt.Printf("Connection failed: %v\n", err)
		} else {
			fmt.Printf("Connected to %d peers\n", len(reachablePeers))
		}
	} else {
		fmt.Println("No reachable peers found")
	}

	stats.TimeTaken = time.Since(startTime)
	return *stats
}

func GetNodeManager() *node.NodeManager {
	return node.GetNodeManagerInterface()
}

func ConnectToTemporaryPeers(peers map[peer.ID]multiaddr.Multiaddr) error {
	nodeManager := GetNodeManager()
	if nodeManager == nil {
		return fmt.Errorf("NodeManager not available")
	}

	var connectedCount, failedCount int
	connectedPeers := make(map[peer.ID]bool)

	for peerID, addr := range peers {
		addrStr := addr.String()
		fmt.Printf("Adding temporary peer for consensus: %s at %s\n", peerID, addrStr)

		if err := nodeManager.AddPeer(addrStr); err != nil {
			// Check if error is because peer is already connected (this is OK)
			if err.Error() == fmt.Sprintf("peer %s is already connected and managed", peerID) {
				fmt.Printf("Peer %s already connected (OK)\n", peerID)
				connectedCount++
				connectedPeers[peerID] = true
			} else {
				fmt.Printf("Failed to add peer %s: %v\n", peerID, err)
				failedCount++
			}
		} else {
			fmt.Printf("Peer %s added to NodeManager for consensus\n", peerID)
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
			fmt.Printf("✅ Verified connection to peer %s\n", peerID.String()[:16])
		} else {
			fmt.Printf("❌ Peer %s not actually connected (status: %v)\n", peerID.String()[:16], connectedness)
			// Remove from connectedPeers map
			delete(connectedPeers, peerID)
		}
	}

	fmt.Printf("Temporary peer connection summary: %d added to NodeManager, %d failed, %d actually connected\n",
		connectedCount, failedCount, actuallyConnected)

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
