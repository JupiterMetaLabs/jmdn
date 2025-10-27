package Cache

import (
	"fmt"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/node"
	"gossipnode/seednode"
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
func FallbackConnectionFunction(peerID peer.ID) {
	client, err := seednode.NewClient(config.SeedNodeURL)
	if err != nil {
		fmt.Printf("[%s] Failed to create seed node client: %v\n", peerID, err)
		return
	}
	defer client.Close()

	peerRecord, err := client.GetPeer(peerID.String())
	if err != nil {
		fmt.Printf("[%s] Failed to get peer from seed node: %v\n", peerID, err)
		return
	}

	reachableFound := false

	for _, multiaddrStr := range peerRecord.Multiaddrs {
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

		reachable, timeTaken, err := nm.PingMultiaddrWithRetries(multiaddrStr, 3) // or call a standalone func
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

// AddPeersTemporary: concurrent reachability + fallback, then single connect
func AddPeersTemporary(peers []PubSubMessages.Buddy_PeerMultiaddr) {
	AddPeersCacheMu.Lock()
	if AddPeersCache == nil {
		AddPeersCache = make(map[peer.ID]multiaddr.Multiaddr)
	}
	AddPeersCacheMu.Unlock()

	var wg sync.WaitGroup
	// Workers can emit up to 2 messages each → buffer accordingly (or just read concurrently)
	results := make(chan string, len(peers)*2)

	fmt.Println("---- Starting concurrent reachability checks ----")

	for idx, buddy := range peers {
		wg.Add(1)
		// Capture buddy values by making a copy for each goroutine
		buddy := buddy
		go func(peerID peer.ID, maddr multiaddr.Multiaddr, index int) {
			defer wg.Done()

			addrStr := maddr.String()
			fmt.Printf("[Thread %d] Checking peer %s at address: %s\n", index, peerID, addrStr)

			nm := GetNodeManager()
			if nm == nil {
				results <- fmt.Sprintf("[%s] NodeManager not available for reachability check", peerID)
				// still try fallback; it does not require nm if your Check is standalone
				FallbackConnectionFunction(peerID)
				results <- fmt.Sprintf("[%s] Fallback triggered (no NodeManager)", peerID)
				return
			}

			reachable, timeTaken, err := nm.PingMultiaddrWithRetries(addrStr, 3)
			if err != nil {
				results <- fmt.Sprintf("[%s] Error checking reachability for %s: %v", peerID, addrStr, err)
				FallbackConnectionFunction(peerID)
				results <- fmt.Sprintf("[%s] Fallback triggered (error in check)", peerID)
				return
			}

			fmt.Printf("[%s] Time taken to check reachability: %v\n", peerID, timeTaken)
			if !reachable {
				results <- fmt.Sprintf("[%s] Multiaddr not reachable, triggering fallback: %s", peerID, addrStr)
				FallbackConnectionFunction(peerID)
				results <- fmt.Sprintf("[%s] Fallback completed (unreachable primary)", peerID)
				return
			}

			AddPeer(peerID, maddr)
			results <- fmt.Sprintf("[%s] Reachable and added: %s", peerID, addrStr)
		}(buddy.PeerID, buddy.Multiaddr, idx)
	}

	// Close results after all workers finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Read results concurrently so writers never block
	fmt.Println("---- Reachability Check Summary ----")
	for res := range results {
		fmt.Println(res)
	}

	total := GetPeerCount()
	fmt.Printf("Total reachable peers in global cache: %d\n", total)

	if total > 0 {
		fmt.Println("---- Connecting to reachable peers ----")
		// Use a snapshot to avoid races if something else modifies the cache
		reachable := GetAllPeers()
		if err := ConnectToTemporaryPeers(reachable); err != nil {
			fmt.Printf("Failed to connect to temporary peers: %v\n", err)
			return
		}
		fmt.Printf("Successfully connected to %d reachable peers\n", len(reachable))
	} else {
		fmt.Println("No reachable peers found — all fallback attempts failed.")
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
