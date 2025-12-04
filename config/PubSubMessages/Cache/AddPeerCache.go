package Cache

import (
	"fmt"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/node"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

var (
	AddPeersCache   map[peer.ID]multiaddr.Multiaddr
	AddPeersCacheMu sync.Mutex // protect global cache
)

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

// AddPeersTemporary: concurrent reachability check, stops at 4 reachable peers
func AddPeersTemporary(peers []PubSubMessages.Buddy_PeerMultiaddr) {
	AddPeersCacheMu.Lock()
	if AddPeersCache == nil {
		AddPeersCache = make(map[peer.ID]multiaddr.Multiaddr)
	}
	AddPeersCacheMu.Unlock()

	const targetPeers = config.MaxMainPeers
	
	var wg sync.WaitGroup
	var found sync.Mutex
	foundCount := 0
	done := make(chan struct{})
	
	reachablePeers := make(map[peer.ID]multiaddr.Multiaddr)
	
	fmt.Println("---- Starting concurrent reachability checks ----")

	nm := GetNodeManager()
	if nm == nil {
		fmt.Println("NodeManager not available")
		return
	}

	for idx, buddy := range peers {
		wg.Add(1)
		go func(peerID peer.ID, maddr multiaddr.Multiaddr, index int) {
			defer wg.Done()
			
			// Check if we already found enough peers
			select {
			case <-done:
				return
			default:
			}

			addrStr := maddr.String()
			fmt.Printf("[Thread %d] Checking peer %s at %s\n", index, peerID, addrStr)

			if nm == nil {
				fmt.Printf("[%s] NodeManager not available\n", peerID)
				return
			}

			reachable, timeTaken, err := nm.PingMultiaddrWithRetries(addrStr, 3)
			if err != nil {
				fmt.Printf("[%s] Error: %v\n", peerID, err)
				return
			}

			fmt.Printf("[%s] Time: %v, Reachable: %v\n", peerID, timeTaken, reachable)
			
			if reachable {
				found.Lock()
				if foundCount < targetPeers {
					reachablePeers[peerID] = maddr
					AddPeer(peerID, maddr)
					foundCount++
					fmt.Printf("✓ Peer %s added (%d/%d)\n", peerID, foundCount, targetPeers)
					
					if foundCount >= targetPeers {
						close(done)
					}
				}
				found.Unlock()
			}
		}(buddy.PeerID, buddy.Multiaddr, idx)
	}

	wg.Wait()
	
	// Ensure done channel is closed
	select {
	case <-done:
	default:
		close(done)
	}

	fmt.Printf("\n---- Found %d/%d reachable peers ----\n", len(reachablePeers), targetPeers)
	
	if len(reachablePeers) > 0 {
		if err := ConnectToTemporaryPeers(reachablePeers); err != nil {
			fmt.Printf("Connection failed: %v\n", err)
			return
		}
		fmt.Printf("Connected to %d peers\n", len(reachablePeers))
	} else {
		fmt.Println("No reachable peers found")
	}
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

	for peerID, addr := range peers {
		addrStr := addr.String()
		fmt.Printf("Adding temporary peer for consensus: %s at %s\n", peerID, addrStr)

		if err := nodeManager.AddPeer(addrStr); err != nil {
			fmt.Printf("Failed to add peer %s: %v\n", peerID, err)
			failedCount++
		} else {
			fmt.Printf("Peer %s added to NodeManager for consensus\n", peerID)
			connectedCount++
		}
	}

	fmt.Printf("Temporary peer connection summary: %d added to NodeManager, %d failed\n", connectedCount, failedCount)
	if failedCount > 0 {
		fmt.Printf("Warning: %d peers failed to be added to NodeManager\n", failedCount)
	}
	return nil
}