package MessagePassing

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gossipnode/AVC/BuddyNodes/CRDTSync"
	"gossipnode/AVC/BuddyNodes/DataLayer"
	Pubsub "gossipnode/Pubsub"
	Publisher "gossipnode/Pubsub/Publish"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/seednode"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// TriggerCRDTSyncForBuddyNode triggers CRDT synchronization for a buddy node
// This ensures the buddy node has the latest CRDT data before processing votes
// Uses mode "both" - publishes local state and subscribes to receive others' state
func TriggerCRDTSyncForBuddyNode(listenerNode *AVCStruct.BuddyNode) error {
	if listenerNode == nil || listenerNode.Host == nil {
		return fmt.Errorf("listener node or host not initialized")
	}

	// Get the pubsub node if available
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if pubSubNode == nil || pubSubNode.PubSub == nil {
		fmt.Printf("⚠️ PubSub node not available, using local CRDT data only\n")
		return nil
	}

	// Get the CRDT layer
	if listenerNode.CRDTLayer == nil {
		return fmt.Errorf("CRDT layer not available")
	}

	// Create sync topic name
	syncConfig := CRDTSync.DefaultSyncConfig()
	topicName := syncConfig.TopicName

	fmt.Printf("🔄 Starting CRDT sync (mode: both - publish & subscribe) on topic: %s\n", topicName)

	// STEP 1: Connect to all buddy nodes before sync starts
	fmt.Printf("🔌 Connecting to buddy nodes for CRDT sync...\n")
	if err := connectToBuddyNodesForSync(listenerNode); err != nil {
		fmt.Printf("⚠️ Failed to connect to some buddy nodes: %v (continuing anyway)\n", err)
	}

	// Create the CRDT sync channel with access control for all buddy nodes
	// If channel doesn't exist, create it as public so all nodes can sync
	if err := Pubsub.CreateChannel(pubSubNode.PubSub, topicName, true, listenerNode.BuddyNodes.Buddies_Nodes); err != nil {
		if err.Error() != fmt.Sprintf("channel %s already exists", topicName) {
			fmt.Printf("⚠️ Failed to create CRDT sync channel: %v\n", err)
			// Try to continue anyway - channel might exist
		} else {
			fmt.Printf("✅ CRDT sync channel already exists\n")
		}
	} else {
		fmt.Printf("✅ Created CRDT sync channel: %s (public, allowing all buddy nodes)\n", topicName)
	}

	// Channel to collect received sync messages
	syncMessages := make(chan CRDTSync.Message, 100)
	syncComplete := make(chan bool, 1)
	var receivedCount int64
	var receivedCountMutex sync.Mutex

	// Subscribe to sync topic to receive CRDT data from other nodes
	// This is the "subscribe" part of "mode both"
	err := Connector.SubscribeEnhanced(pubSubNode.PubSub, topicName, func(gossipMsg *AVCStruct.GossipMessage) {
		if gossipMsg == nil || gossipMsg.Data == nil {
			return
		}

		// Parse the message content
		// Use a flexible message struct that can handle different timestamp formats
		var rawMsg map[string]json.RawMessage
		messageBytes := []byte(gossipMsg.Data.Message)

		if err := json.Unmarshal(messageBytes, &rawMsg); err != nil {
			fmt.Printf("⚠️ Failed to parse CRDT sync message (raw): %v\n", err)
			return
		}

		// Build the CRDT sync message manually to handle flexible timestamp
		crdtSyncMsg := CRDTSync.Message{}

		if val, ok := rawMsg["type"]; ok {
			json.Unmarshal(val, &crdtSyncMsg.Type)
		}
		if val, ok := rawMsg["node_id"]; ok {
			json.Unmarshal(val, &crdtSyncMsg.NodeID)
		}
		if val, ok := rawMsg["key"]; ok {
			json.Unmarshal(val, &crdtSyncMsg.Key)
		}
		if val, ok := rawMsg["sync_data"]; ok {
			json.Unmarshal(val, &crdtSyncMsg.SyncData)
		}

		// Handle timestamp - could be Unix int64 or RFC3339 string
		if val, ok := rawMsg["timestamp"]; ok {
			// Try Unix timestamp first (int64)
			var unixTS int64
			if err := json.Unmarshal(val, &unixTS); err == nil {
				crdtSyncMsg.Timestamp = time.Unix(unixTS, 0)
			} else {
				// Try RFC3339 string format
				var timeStr string
				if err := json.Unmarshal(val, &timeStr); err == nil {
					if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
						crdtSyncMsg.Timestamp = t
					} else if t, err := time.Parse(time.RFC3339Nano, timeStr); err == nil {
						crdtSyncMsg.Timestamp = t
					}
				} else {
					// Try direct time.Time unmarshal
					json.Unmarshal(val, &crdtSyncMsg.Timestamp)
				}
			}
		}

		// Skip our own messages
		if crdtSyncMsg.NodeID == listenerNode.PeerID.String() {
			return
		}

		// Only process actual sync messages (with sync_data)
		if crdtSyncMsg.Type == config.Type_CRDT_SYNC && crdtSyncMsg.SyncData != nil {
			receivedCountMutex.Lock()
			receivedCount++
			count := receivedCount
			receivedCountMutex.Unlock()

			fmt.Printf("📥 Received CRDT sync from %s (%d messages received)\n", crdtSyncMsg.NodeID[:8], count)
			syncMessages <- crdtSyncMsg

			// For eventual consistency: wait for messages from at least half the buddy nodes
			// or wait for timeout (whichever comes first)
			buddyCount := len(listenerNode.BuddyNodes.Buddies_Nodes)
			if buddyCount == 0 {
				buddyCount = 1 // Prevent division by zero
			}
			minRequired := buddyCount / 2
			if minRequired < 1 {
				minRequired = 1 // At least 1 message
			}

			if count >= int64(minRequired) {
				// We have enough messages for eventual consistency
				select {
				case syncComplete <- true:
				default:
				}
			}
		}
	})

	if err != nil {
		fmt.Printf("⚠️ Failed to subscribe to CRDT sync topic: %v\n", err)
		return fmt.Errorf("failed to subscribe to sync topic: %w", err)
	}

	// Publish our own CRDT state (mode "both" - publish part)
	allCRDTs := listenerNode.CRDTLayer.CRDTLayer.GetAllCRDTs()
	fmt.Printf("📤 Publishing local CRDT state (%d objects)\n", len(allCRDTs))

	if len(allCRDTs) > 0 {
		syncData := make(map[string]json.RawMessage)
		for key, crdt := range allCRDTs {
			data, err := json.Marshal(crdt)
			if err != nil {
				continue
			}
			syncData[key] = data
		}

		// Create sync message using CRDTSync.Message struct directly for proper marshaling
		syncMsg := CRDTSync.Message{
			Type:      config.Type_CRDT_SYNC,
			NodeID:    listenerNode.PeerID.String(),
			Key:       "all-crdts",
			SyncData:  syncData,
			Timestamp: time.Now(),
		}

		syncDataBytes, err := json.Marshal(syncMsg)
		if err == nil {
			if err := Publisher.Publish(pubSubNode.PubSub, topicName,
				AVCStruct.NewMessageBuilder(nil).
					SetSender(listenerNode.PeerID).
					SetMessage(string(syncDataBytes)).
					SetTimestamp(time.Now().Unix()).
					SetACK(AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_CRDT_SYNC)),
				nil); err != nil {
				fmt.Printf("⚠️ Failed to publish CRDT sync: %v\n", err)
			}
		}
	}

	// Wait for sync messages and merge them
	fmt.Printf("⏳ Waiting for CRDT sync messages from other nodes...\n")
	timeout := time.After(5 * time.Second)
	mergedCount := 0

	for {
		select {
		case syncMsg := <-syncMessages:
			// Merge received CRDT data into local CRDT
			if err := mergeCRDTData(listenerNode, syncMsg); err != nil {
				fmt.Printf("⚠️ Failed to merge CRDT from %s: %v\n", syncMsg.NodeID[:8], err)
			} else {
				mergedCount++
				fmt.Printf("✅ Merged CRDT data from %s (total merged: %d)\n", syncMsg.NodeID[:8], mergedCount)
			}

		case <-syncComplete:
			fmt.Printf("✅ Received sync messages from all buddy nodes\n")
			// Give a moment for any remaining messages
			time.Sleep(1 * time.Second)
			goto SyncDone

		case <-timeout:
			fmt.Printf("⏱️ Sync timeout - processed %d messages\n", mergedCount)
			goto SyncDone
		}
	}

SyncDone:
	// Process any remaining messages in the channel (non-blocking)
	processedRemaining := 0
	for processedRemaining < 50 {
		select {
		case syncMsg := <-syncMessages:
			if err := mergeCRDTData(listenerNode, syncMsg); err == nil {
				mergedCount++
				processedRemaining++
			}
		default:
			// No more messages available
			processedRemaining = 50 // Exit loop
		}
		if processedRemaining >= 50 {
			break
		}
	}

	fmt.Printf("✅ CRDT sync completed - merged data from %d nodes\n", mergedCount)
	return nil
}

// connectToBuddyNodesForSync connects to all buddy nodes before CRDT sync
// This ensures nodes are connected via libp2p so pubsub messages can be delivered
func connectToBuddyNodesForSync(listenerNode *AVCStruct.BuddyNode) error {
	if listenerNode == nil || listenerNode.Host == nil {
		return fmt.Errorf("listener node or host not initialized")
	}

	// Get buddy nodes from multiple sources (avoid duplicates)
	buddyPeerIDs := make([]peer.ID, 0)
	seenPeers := make(map[string]bool)

	addPeerIfNew := func(peerID peer.ID) {
		peerIDStr := peerID.String()
		if !seenPeers[peerIDStr] && peerID != listenerNode.PeerID {
			buddyPeerIDs = append(buddyPeerIDs, peerID)
			seenPeers[peerIDStr] = true
		}
	}

	// Source 1: Check listenerNode.BuddyNodes.Buddies_Nodes
	initialCount := len(buddyPeerIDs)
	for _, peerID := range listenerNode.BuddyNodes.Buddies_Nodes {
		addPeerIfNew(peerID)
	}
	if len(buddyPeerIDs) > initialCount {
		fmt.Printf("📋 Found %d buddy nodes from listenerNode.BuddyNodes\n", len(buddyPeerIDs)-initialCount)
	}

	// Source 2: Get from consensus message cache (Buddy_PeerMultiaddr with multiaddrs)
	initialCount = len(buddyPeerIDs)
	for _, consensusMsg := range AVCStruct.CacheConsensuMessage {
		if consensusMsg != nil && consensusMsg.Buddies != nil {
			for _, buddy := range consensusMsg.Buddies {
				addPeerIfNew(buddy.PeerID)
			}
		}
	}
	if len(buddyPeerIDs) > initialCount {
		fmt.Printf("📋 Found %d buddy nodes from consensus message cache\n", len(buddyPeerIDs)-initialCount)
	}

	// Source 3: Get from connected peers on the network (as fallback)
	initialCount = len(buddyPeerIDs)
	if len(buddyPeerIDs) == 0 {
		connectedPeers := listenerNode.Host.Network().Peers()
		for _, peerID := range connectedPeers {
			addPeerIfNew(peerID)
		}
		if len(buddyPeerIDs) > initialCount {
			fmt.Printf("📋 Found %d buddy nodes from connected peers\n", len(buddyPeerIDs)-initialCount)
		}
	}

	if len(buddyPeerIDs) == 0 {
		fmt.Printf("⚠️ No buddy nodes found from any source\n")
		fmt.Printf("⚠️ Cannot connect to other nodes for CRDT sync\n")
		return nil
	}

	fmt.Printf("✅ Total buddy nodes to connect: %d\n", len(buddyPeerIDs))

	fmt.Printf("🔌 Connecting to %d buddy nodes for CRDT sync...\n", len(buddyPeerIDs))

	connectedCount := 0
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Connect to each buddy node
	for _, buddyPeerID := range buddyPeerIDs {
		// Skip self
		if buddyPeerID == listenerNode.PeerID {
			continue
		}

		// Check if already connected
		if listenerNode.Host.Network().Connectedness(buddyPeerID) == network.Connected {
			fmt.Printf("✅ Already connected to buddy %s\n", buddyPeerID.String()[:8])
			connectedCount++
			continue
		}

		var multiaddrs []multiaddr.Multiaddr

		// Priority 1: Check consensus message cache (has Buddy_PeerMultiaddr with multiaddrs)
		for _, consensusMsg := range AVCStruct.CacheConsensuMessage {
			if consensusMsg != nil && consensusMsg.Buddies != nil {
				for _, buddy := range consensusMsg.Buddies {
					if buddy.PeerID == buddyPeerID && buddy.Multiaddr != nil {
						multiaddrs = []multiaddr.Multiaddr{buddy.Multiaddr}
						fmt.Printf("📋 Got multiaddr from consensus cache for buddy %s: %s\n", buddyPeerID.String()[:8], buddy.Multiaddr.String())
						break
					}
				}
				if len(multiaddrs) > 0 {
					break
				}
			}
		}

		// Priority 2: Try to get from peerstore (fastest local source)
		if len(multiaddrs) == 0 {
			peerstoreAddrs := listenerNode.Host.Peerstore().Addrs(buddyPeerID)
			if len(peerstoreAddrs) > 0 {
				multiaddrs = peerstoreAddrs
				fmt.Printf("📋 Got %d multiaddrs from peerstore for buddy %s\n", len(multiaddrs), buddyPeerID.String()[:8])
			}
		}

		// Priority 3: Query seed node as last resort
		if len(multiaddrs) == 0 && config.SeedNodeURL != "" {
			fmt.Printf("🔍 Querying seed node for multiaddr of buddy %s...\n", buddyPeerID.String()[:8])

			client, err := seednode.NewClient(config.SeedNodeURL)
			if err == nil {
				// Try to get peer record from seed node
				peerRecord, err := client.GetPeer(buddyPeerID.String())
				if err == nil && peerRecord != nil && len(peerRecord.GetMultiaddrs()) > 0 {
					for _, addrStr := range peerRecord.GetMultiaddrs() {
						if maddr, err := multiaddr.NewMultiaddr(addrStr); err == nil {
							multiaddrs = append(multiaddrs, maddr)
						}
					}
					fmt.Printf("📋 Got %d multiaddrs from seed node for buddy %s\n", len(multiaddrs), buddyPeerID.String()[:8])
				} else if err != nil {
					fmt.Printf("⚠️ Failed to get peer from seed node: %v\n", err)
				}
			} else {
				fmt.Printf("⚠️ Failed to create seed node client: %v\n", err)
			}
		}

		// Attempt connection
		if len(multiaddrs) > 0 {
			peerInfo := peer.AddrInfo{
				ID:    buddyPeerID,
				Addrs: multiaddrs,
			}

			fmt.Printf("🔌 Attempting to connect to buddy %s at %s...\n", buddyPeerID.String()[:8], multiaddrs[0].String())

			if err := listenerNode.Host.Connect(ctx, peerInfo); err != nil {
				fmt.Printf("❌ Failed to connect to buddy %s: %v\n", buddyPeerID.String()[:8], err)
				// Try next multiaddr if available
				if len(multiaddrs) > 1 {
					for i := 1; i < len(multiaddrs) && i < 3; i++ { // Try up to 3 addresses
						peerInfo.Addrs = []multiaddr.Multiaddr{multiaddrs[i]}
						if err := listenerNode.Host.Connect(ctx, peerInfo); err == nil {
							fmt.Printf("✅ Connected to buddy %s using fallback address\n", buddyPeerID.String()[:8])
							connectedCount++
							goto nextPeer
						}
					}
				}
			} else {
				fmt.Printf("✅ Connected to buddy %s\n", buddyPeerID.String()[:8])
				connectedCount++
			}
		} else {
			fmt.Printf("⚠️ No multiaddrs found for buddy %s, skipping connection\n", buddyPeerID.String()[:8])
		}

	nextPeer:
		// Small delay between connections
		time.Sleep(100 * time.Millisecond)
	}

	fmt.Printf("✅ Connected to %d/%d buddy nodes for CRDT sync\n", connectedCount, len(buddyPeerIDs))

	// Wait a moment for connections to establish
	time.Sleep(1 * time.Second)

	return nil
}

// mergeCRDTData merges received CRDT data into the local CRDT layer
// The key is the peer ID, and elements are vote JSON strings
func mergeCRDTData(listenerNode *AVCStruct.BuddyNode, syncMsg CRDTSync.Message) error {
	if listenerNode.CRDTLayer == nil || listenerNode.CRDTLayer.CRDTLayer == nil {
		return fmt.Errorf("CRDT layer not available")
	}

	// Get the sender's peer ID (who sent this sync message)
	senderPeerID, err := peer.Decode(syncMsg.NodeID)
	if err != nil {
		return fmt.Errorf("invalid sender peer ID: %w", err)
	}

	fmt.Printf("🔄 Merging CRDT data from peer %s\n", senderPeerID.String()[:8])

	// Merge each CRDT from the sync message
	// Key is the vote peer ID, value is the CRDT set containing vote elements
	for votePeerIDStr, rawData := range syncMsg.SyncData {
		// Parse the vote peer ID
		votePeerID, err := peer.Decode(votePeerIDStr)
		if err != nil {
			fmt.Printf("⚠️ Invalid peer ID in sync data: %s\n", votePeerIDStr)
			continue
		}

		// Unmarshal the CRDT structure (LWWSet)
		var remoteCRDT struct {
			Key     string                 `json:"key"`
			Adds    map[string]interface{} `json:"adds"`
			Removes map[string]interface{} `json:"removes"`
		}

		if err := json.Unmarshal(rawData, &remoteCRDT); err != nil {
			fmt.Printf("⚠️ Failed to unmarshal CRDT for peer %s: %v\n", votePeerIDStr[:8], err)
			continue
		}

		// Extract all elements from the Adds map (these are the vote JSON strings)
		if remoteCRDT.Adds != nil {
			for element := range remoteCRDT.Adds {
				// Add this vote element to our local CRDT
				// DataLayer.Add(controller, nodeID peer.ID, key string, value string)
				// For votes: key is the vote peer ID, value is the vote JSON element
				if err := DataLayer.Add(listenerNode.CRDTLayer, votePeerID, votePeerIDStr, element); err != nil {
					fmt.Printf("⚠️ Failed to add vote element to CRDT for peer %s: %v\n", votePeerIDStr[:8], err)
				} else {
					if len(element) > 50 {
						fmt.Printf("  ✅ Added vote element from peer %s: %s...\n", votePeerIDStr[:8], element[:50])
					} else {
						fmt.Printf("  ✅ Added vote element from peer %s: %s\n", votePeerIDStr[:8], element)
					}
				}
			}
		}
	}

	fmt.Printf("✅ Completed merging CRDT data from peer %s\n", senderPeerID.String()[:8])

	return nil
}
