package MessagePassing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"gossipnode/AVC/BuddyNodes/CRDTSync"
	"gossipnode/AVC/BuddyNodes/DataLayer"
	Publisher "gossipnode/Pubsub/Publish"
	Connector "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"
	"gossipnode/crdt"
	"gossipnode/seednode"

	avcdatalayer "github.com/JupiterMetaLabs/avc/buddynodes/datalayer"
	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog/log"
)

// TriggerCRDTSyncForBuddyNode triggers CRDT synchronization for a buddy node
// This ensures the buddy node has the latest CRDT data before processing votes
// Uses mode "both" - publishes local state and subscribes to receive others' state
func TriggerCRDTSyncForBuddyNode(logger_ctx context.Context, listenerNode *AVCStruct.BuddyNode) error {
	if listenerNode == nil || listenerNode.Host == nil {
		return fmt.Errorf("listener node or host not initialized")
	}

	// Get the pubsub node if available
	pubSubNode := AVCStruct.NewGlobalVariables().Get_PubSubNode()
	if pubSubNode == nil || pubSubNode.PubSub == nil {
		logger().Info(context.Background(), "⚠️ PubSub node not available, using local CRDT data only")
		return nil
	}

	// Get the CRDT layer
	if listenerNode.CRDTLayer == nil {
		return fmt.Errorf("CRDT layer not available")
	}

	// Ensure buddy nodes list is populated from cached consensus if empty
	if len(listenerNode.BuddyNodes.Buddies_Nodes) == 0 {
		logger().Info(context.Background(), "⚠️ Buddy list empty at CRDT sync; attempting to populate from consensus cache")
		buddyIDs := make([]peer.ID, 0, config.MaxMainPeers)
		count := 0
		for _, consensusMsg := range AVCStruct.SnapshotConsensusMessages() {
			if consensusMsg == nil || consensusMsg.Buddies == nil {
				continue
			}
			for i := 0; i < config.MaxMainPeers && i < len(consensusMsg.Buddies); i++ {
				if b, ok := consensusMsg.Buddies[i]; ok {
					if b.PeerID != listenerNode.PeerID {
						buddyIDs = append(buddyIDs, b.PeerID)
						count++
						if count >= config.MaxMainPeers {
							break
						}
					}
				}
			}
			if count >= config.MaxMainPeers {
				break
			}
		}
		if len(buddyIDs) > 0 {
			listenerNode.BuddyNodes.Buddies_Nodes = buddyIDs
			logger().Info(context.Background(), "✅ Populated buddy nodes from cache for CRDT sync:", ion.String("args", fmt.Sprintf("✅ Populated buddy nodes from cache for CRDT sync: %d peers (MaxMainPeers=%d)", len(buddyIDs), config.MaxMainPeers)))
		}
	}

	// Create sync topic name
	syncConfig := CRDTSync.DefaultSyncConfig()
	topicName := syncConfig.TopicName

	logger().Info(context.Background(), "🔄 Starting CRDT sync (mode: both - publish & subscribe) on topic:", ion.String("args", fmt.Sprintf("🔄 Starting CRDT sync (mode: both - publish & subscribe) on topic: %s", topicName)))

	// STEP 1: Connect to all buddy nodes before sync starts
	logger().Info(context.Background(), "🔌 Connecting to buddy nodes for CRDT sync...")
	if err := connectToBuddyNodesForSync(listenerNode); err != nil {
		log.Warn().
			Str("self", listenerNode.PeerID.String()).
			Err(err).
			Msg("CRDT sync: failed to connect to some buddy nodes before sync started; continuing anyway")
	}

	// Note: The CRDT sync channel is created by the sequencer during consensus start
	// ONLY vote aggregating buddy nodes can join this channel (not regular network nodes)
	// Buddy nodes should only subscribe to it, not create it
	// This ensures all vote aggregating nodes join the same channel created by the sequencer
	logger().Info(context.Background(), "📡 Subscribing to CRDT sync channel (private channel for vote aggregating buddies):", ion.String("args", fmt.Sprintf("📡 Subscribing to CRDT sync channel (private channel for vote aggregating buddies): %s", topicName)))

	// Create local channel reference if it doesn't exist (for subscription permission check)
	// This is just a local representation - the actual channel is created by the sequencer
	// The actual channel is private (isPublic: false) and only allows sequencer + selected buddy nodes
	pubSubNode.PubSub.Mutex.Lock()
	if _, exists := pubSubNode.PubSub.ChannelAccess[topicName]; !exists {
		// Channel doesn't exist locally, create a local reference
		// Note: This node must be in the sequencer's allowed peers list to actually subscribe
		allowedMap := make(map[peer.ID]bool)
		allowedMap[pubSubNode.PubSub.Host.ID()] = true

		pubSubNode.PubSub.ChannelAccess[topicName] = &AVCStruct.ChannelAccess{
			ChannelName:  topicName,
			AllowedPeers: allowedMap,
			IsPublic:     false, // Private channel - only allowed peers (vote aggregating buddies) can subscribe
			Creator:      pubSubNode.PubSub.Host.ID(),
			CreatedAt:    time.Now().UTC().Unix(),
		}
		logger().Info(context.Background(), "📋 Created local channel reference for", ion.String("args", fmt.Sprintf("📋 Created local channel reference for %s (private, only vote aggregating buddies allowed)", topicName)))
	}
	pubSubNode.PubSub.Mutex.Unlock()

	// IMPORTANT: Only sync with config.MaxMainPeers buddy nodes (the vote aggregating nodes)
	// NOT all nodes in the network - we want exactly MaxMainPeers nodes for CRDT sync
	expectedBuddyCount := config.MaxMainPeers

	// Get buddy nodes - only use the first MaxMainPeers nodes
	// This ensures we sync with the same set of nodes that are performing vote aggregation
	buddyNodeIDs := make(map[string]bool)
	allBuddyNodes := make([]peer.ID, 0)

	// Take only the first MaxMainPeers buddy nodes (excluding self)
	for i, buddyID := range listenerNode.BuddyNodes.Buddies_Nodes {
		if i >= expectedBuddyCount {
			break // Only use MaxMainPeers nodes
		}
		if buddyID != listenerNode.PeerID {
			buddyIDStr := buddyID.String()
			if !buddyNodeIDs[buddyIDStr] {
				buddyNodeIDs[buddyIDStr] = true
				allBuddyNodes = append(allBuddyNodes, buddyID)
			}
		}
	}

	totalBuddyNodes := len(allBuddyNodes)
	if totalBuddyNodes == 0 {
		logger().Info(context.Background(), "⚠️ No other buddy nodes found (expected", ion.String("args", fmt.Sprintf("⚠️ No other buddy nodes found (expected %d) - skipping CRDT sync", expectedBuddyCount)))
		return nil
	}

	if totalBuddyNodes < expectedBuddyCount {
		logger().Info(context.Background(), "⚠️ Only found", ion.String("args", fmt.Sprintf("⚠️ Only found %d buddy nodes, expected %d (config.MaxMainPeers)", totalBuddyNodes, expectedBuddyCount)))
	}

	logger().Info(context.Background(), "📋 Will sync with", ion.String("args", fmt.Sprintf("📋 Will sync with %d buddy nodes (expected: %d from config.MaxMainPeers)", totalBuddyNodes, expectedBuddyCount)))

	// Track received messages from each buddy node
	receivedFrom := make(map[string]bool)
	receivedMutex := sync.Mutex{}
	syncMessages := make(chan CRDTSync.Message, 100)
	syncComplete := make(chan bool, 1)

	// Subscribe to sync topic to receive CRDT data from other nodes
	// This is the "subscribe" part of "mode both"
	err := Connector.SubscribeEnhanced(logger_ctx, pubSubNode.PubSub, topicName, func(gossipMsg *AVCStruct.GossipMessage) {
		if gossipMsg == nil || gossipMsg.Data == nil {
			return
		}

		// Parse the message content
		var rawMsg map[string]json.RawMessage
		messageBytes := []byte(gossipMsg.Data.Message)

		if err := json.Unmarshal(messageBytes, &rawMsg); err != nil {
			logger().Info(context.Background(), "⚠️ Failed to parse CRDT sync message (raw):", ion.String("args", fmt.Sprintf("⚠️ Failed to parse CRDT sync message (raw): %v", err)))
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
			var unixTS int64
			if err := json.Unmarshal(val, &unixTS); err == nil {
				crdtSyncMsg.Timestamp = time.Unix(unixTS, 0)
			} else {
				var timeStr string
				if err := json.Unmarshal(val, &timeStr); err == nil {
					if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
						crdtSyncMsg.Timestamp = t
					} else if t, err := time.Parse(time.RFC3339Nano, timeStr); err == nil {
						crdtSyncMsg.Timestamp = t
					}
				} else {
					json.Unmarshal(val, &crdtSyncMsg.Timestamp)
				}
			}
		}

		// Skip our own messages
		if crdtSyncMsg.NodeID == listenerNode.PeerID.String() {
			return
		}

		// Only process sync messages from known buddy nodes
		if !buddyNodeIDs[crdtSyncMsg.NodeID] {
			return
		}

		// Only process actual sync messages (with sync_data)
		if crdtSyncMsg.Type == config.Type_CRDT_SYNC && crdtSyncMsg.SyncData != nil {
			receivedMutex.Lock()
			// Check if we've already received from this node
			if !receivedFrom[crdtSyncMsg.NodeID] {
				receivedFrom[crdtSyncMsg.NodeID] = true
				count := len(receivedFrom)
				receivedMutex.Unlock()

				logger().Info(context.Background(), fmt.Sprintf("📥 Received CRDT sync from %s (%d/%d buddy nodes)", crdtSyncMsg.NodeID[:8], count, totalBuddyNodes))
				syncMessages <- crdtSyncMsg

				// Check if we've received from all buddy nodes
				if count >= totalBuddyNodes {
					logger().Info(context.Background(), "✅ Received CRDT sync from all", ion.String("args", fmt.Sprintf("✅ Received CRDT sync from all %d buddy nodes - ready to complete", totalBuddyNodes)))
					select {
					case syncComplete <- true:
					default:
					}
				}
			} else {
				receivedMutex.Unlock()
				// Already received from this node, skip duplicate
			}
		}
	})

	if err != nil {
		log.Error().
			Str("self", listenerNode.PeerID.String()).
			Str("topic", topicName).
			Err(err).
			Msg("CRDT sync: failed to subscribe to sync topic; sync aborted for this request")
		return fmt.Errorf("failed to subscribe to sync topic: %w", err)
	}

	// Publish our own CRDT state ONCE to the pubsub channel. Stage 3
	// (docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md) — buildLocalSyncData combines
	// the legacy peer-keyed engine and the new block-keyed vote engine into
	// one map, so this stays one topic, one message, one round trip. Safe to
	// combine: the two keyspaces cannot collide (votes.OwnsKey's own
	// guarantee — see its doc comment).
	syncData := buildLocalSyncData(listenerNode)
	logger().Info(context.Background(), "📤 Publishing local CRDT state (", ion.String("args", fmt.Sprintf("📤 Publishing local CRDT state (%d objects) to pubsub channel: %s", len(syncData), topicName)))

	if len(syncData) > 0 {

		// Create sync message
		syncMsg := CRDTSync.Message{
			Type:      config.Type_CRDT_SYNC,
			NodeID:    listenerNode.PeerID.String(),
			Key:       "all-crdts",
			SyncData:  syncData,
			Timestamp: time.Now().UTC(),
		}

		syncDataBytes, err := json.Marshal(syncMsg)
		if err != nil {
			log.Error().
				Str("self", listenerNode.PeerID.String()).
				Err(err).
				Msg("CRDT sync: failed to marshal local sync message; own state was NOT published this round")
		} else {
			if err := Publisher.Publish(logger_ctx, pubSubNode.PubSub, topicName,
				AVCStruct.NewMessageBuilder(nil).
					SetSender(listenerNode.PeerID).
					SetMessage(string(syncDataBytes)).
					SetTimestamp(time.Now().UTC().Unix()).
					SetACK(AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_CRDT_SYNC)),
				nil); err != nil {
				log.Error().
					Str("self", listenerNode.PeerID.String()).
					Str("topic", topicName).
					Err(err).
					Msg("CRDT sync: failed to publish local CRDT state; own state was NOT published this round")
			} else {
				logger().Info(context.Background(), "✅ Published CRDT state to pubsub channel")
			}
		}
	} else {
		logger().Info(context.Background(), "⚠️ No CRDT objects to publish (empty CRDT)")
		// Still publish an empty sync message so other nodes know we're active
		syncMsg := CRDTSync.Message{
			Type:      config.Type_CRDT_SYNC,
			NodeID:    listenerNode.PeerID.String(),
			Key:       "all-crdts",
			SyncData:  make(map[string]json.RawMessage),
			Timestamp: time.Now().UTC(),
		}
		syncDataBytes, _ := json.Marshal(syncMsg)
		Publisher.Publish(logger_ctx, pubSubNode.PubSub, topicName,
			AVCStruct.NewMessageBuilder(nil).
				SetSender(listenerNode.PeerID).
				SetMessage(string(syncDataBytes)).
				SetTimestamp(time.Now().UTC().Unix()).
				SetACK(AVCStruct.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_CRDT_SYNC)),
			nil)
	}

	// Wait for sync messages from all buddy nodes and merge them
	// Keep the pubsub channel open for full 30 seconds to ensure all nodes sync
	// Increased from 10s to 30s to handle network delays
	syncDuration := 30 * time.Second
	logger().Info(context.Background(), "⏳ Waiting for CRDT sync messages from", ion.String("args", fmt.Sprintf("⏳ Waiting for CRDT sync messages from %d buddy nodes", totalBuddyNodes)))
	logger().Info(context.Background(), "Pubsub channel will stay open for", ion.String("args", fmt.Sprintf("Pubsub channel will stay open for %v to ensure complete synchronization", syncDuration)))

	startTime := time.Now().UTC()
	timeout := time.After(syncDuration)
	mergedCount := 0
	var subscriptionDone bool

	// Track periodic updates
	lastUpdate := time.Now().UTC()

	for !subscriptionDone {
		select {
		case syncMsg := <-syncMessages:
			// Merge received CRDT data into local CRDT
			if err := mergeCRDTData(listenerNode, syncMsg); err != nil {
				log.Warn().
					Str("self", listenerNode.PeerID.String()).
					Str("from_node_id", syncMsg.NodeID).
					Err(err).
					Msg("CRDT sync: failed to merge sync data received from a buddy; that buddy's state was NOT merged this round")
			} else {
				mergedCount++
				receivedMutex.Lock()
				receivedCount := len(receivedFrom)
				receivedMutex.Unlock()

				elapsed := time.Since(startTime)
				logger().Info(context.Background(), fmt.Sprintf("✅ Merged CRDT from %s (%d/%d merged, %d/%d received, elapsed: %v)", syncMsg.NodeID[:8], mergedCount, totalBuddyNodes, receivedCount, totalBuddyNodes, elapsed.Round(time.Second)))

				// Check if we've received from all buddy nodes
				if receivedCount >= totalBuddyNodes {
					// Received from all, but keep subscription open for remaining time to catch any late messages
					remaining := syncDuration - elapsed
					if remaining > 0 && time.Since(lastUpdate) > 2*time.Second {
						logger().Info(context.Background(), fmt.Sprintf("📥 Received from all %d buddies, keeping channel open for %v more to ensure full sync", totalBuddyNodes, remaining.Round(time.Second)))
						lastUpdate = time.Now().UTC()
					}
				}
			}

		case <-syncComplete:
			elapsed := time.Since(startTime)
			logger().Info(context.Background(), fmt.Sprintf("✅ Received sync messages from all %d buddy nodes (elapsed: %v)", totalBuddyNodes, elapsed.Round(time.Second)))
			// Keep subscription open until timeout to ensure we receive all messages
			remaining := syncDuration - elapsed
			if remaining > 0 {
				logger().Info(context.Background(), "Keeping channel open for", ion.String("args", fmt.Sprintf("Keeping channel open for %v more to catch any late messages", remaining.Round(time.Second))))
			}

		case <-timeout:
			receivedMutex.Lock()
			receivedCount := len(receivedFrom)
			missing := make([]string, 0, totalBuddyNodes-receivedCount)
			for _, buddyID := range allBuddyNodes {
				if !receivedFrom[buddyID.String()] {
					missing = append(missing, buddyID.String())
				}
			}
			receivedMutex.Unlock()
			elapsed := time.Since(startTime)
			if receivedCount < totalBuddyNodes {
				// Degraded sync: some buddies never responded within the 30s
				// window. This is the case an operator most needs to find —
				// logged at Warn, with exactly which buddy IDs are missing,
				// so a stalled/slow/unreachable buddy is identifiable
				// without cross-referencing separate per-message logs.
				log.Warn().
					Str("self", listenerNode.PeerID.String()).
					Dur("elapsed", elapsed.Round(time.Second)).
					Int("received", receivedCount).
					Int("expected", totalBuddyNodes).
					Int("merged", mergedCount).
					Strs("missing_buddy_ids", missing).
					Msg("CRDT sync: 30s window ended with buddies still missing")
			} else {
				log.Info().
					Str("self", listenerNode.PeerID.String()).
					Dur("elapsed", elapsed.Round(time.Second)).
					Int("received", receivedCount).
					Int("expected", totalBuddyNodes).
					Int("merged", mergedCount).
					Msg("CRDT sync: 30s window ended, all expected buddies had responded")
			}
			subscriptionDone = true
		}

		// Periodic status update every 2 seconds
		if time.Since(lastUpdate) > 2*time.Second && !subscriptionDone {
			receivedMutex.Lock()
			receivedCount := len(receivedFrom)
			receivedMutex.Unlock()
			elapsed := time.Since(startTime)
			remaining := syncDuration - elapsed
			if remaining > 0 {
				logger().Info(context.Background(), fmt.Sprintf("📊 Sync status: %d/%d received, %d merged, %v remaining", receivedCount, totalBuddyNodes, mergedCount, remaining.Round(time.Second)))
				lastUpdate = time.Now().UTC()
			}
		}
	}

	// Process any remaining messages in the channel (non-blocking, quick drain)
	logger().Info(context.Background(), "🔄 Processing any remaining messages...")
	remainingProcessed := 0
	drainTimeout := time.After(2 * time.Second)
drainLoop:
	for remainingProcessed < 100 {
		select {
		case syncMsg := <-syncMessages:
			if err := mergeCRDTData(listenerNode, syncMsg); err == nil {
				mergedCount++
				remainingProcessed++
			}
		case <-drainTimeout:
			break drainLoop
		default:
			// Channel empty or timeout
			break drainLoop
		}
	}

	logger().Info(context.Background(), "═══════════════════════════════════════════════════════════")
	logger().Info(context.Background(), "✅ CRDT SYNC COMPLETE - Exchanged states with", ion.String("args", fmt.Sprintf("✅ CRDT SYNC COMPLETE - Exchanged states with %d buddy nodes", mergedCount)))
	logger().Info(context.Background(), "All buddy nodes should now have consistent CRDT data")
	logger().Info(context.Background(), "═══════════════════════════════════════════════════════════")

	return nil
}

// connectToBuddyNodesForSync connects to all buddy nodes before CRDT sync
// This ensures nodes are connected via libp2p so pubsub messages can be delivered
func connectToBuddyNodesForSync(listenerNode *AVCStruct.BuddyNode) error {
	if listenerNode == nil || listenerNode.Host == nil {
		return fmt.Errorf("listener node or host not initialized")
	}

	// IMPORTANT: Only connect to config.MaxMainPeers buddy nodes for CRDT sync
	// NOT all nodes in the network - we want exactly MaxMainPeers nodes
	expectedBuddyCount := config.MaxMainPeers

	// Prefer multiaddr-based targets taken directly from cached consensus message
	// This avoids relying on peerstore-only lookups and ensures we dial using explicit multiaddrs
	buddyTargets := make([]AVCStruct.Buddy_PeerMultiaddr, 0, expectedBuddyCount)
	seenPeers := make(map[string]bool)

	// Source 1: Use consensus cache with explicit multiaddrs
	cacheAdded := 0
	for _, consensusMsg := range AVCStruct.SnapshotConsensusMessages() {
		if consensusMsg == nil || consensusMsg.Buddies == nil {
			continue
		}
		for i := 0; i < expectedBuddyCount && i < len(consensusMsg.Buddies); i++ {
			if b, ok := consensusMsg.Buddies[i]; ok {
				if b.PeerID == listenerNode.PeerID {
					continue
				}
				pid := b.PeerID.String()
				if !seenPeers[pid] && b.Multiaddr != nil {
					buddyTargets = append(buddyTargets, b)
					seenPeers[pid] = true
					cacheAdded++
					if len(buddyTargets) >= expectedBuddyCount {
						break
					}
				}
			}
		}
		if len(buddyTargets) >= expectedBuddyCount {
			break
		}
	}
	if cacheAdded > 0 {
		logger().Info(context.Background(), "📋 Using", ion.String("args", fmt.Sprintf("📋 Using %d buddy targets from consensus cache (multiaddr-based)", cacheAdded)))
	}

	// NOTE: We do NOT use connected peers as fallback anymore
	// This was causing us to include all network nodes (18-20) instead of just MaxMainPeers (4)
	// We rely only on the sequencer-populated buddy node list

	// If we still have no targets, fall back to peer IDs from listenerNode (will resolve addrs later)
	if len(buddyTargets) == 0 {
		fallbackIDs := make([]peer.ID, 0, expectedBuddyCount)
		for i, pid := range listenerNode.BuddyNodes.Buddies_Nodes {
			if i >= expectedBuddyCount {
				break
			}
			if pid != listenerNode.PeerID && !seenPeers[pid.String()] {
				fallbackIDs = append(fallbackIDs, pid)
				seenPeers[pid.String()] = true
			}
		}
		if len(fallbackIDs) == 0 {
			logger().Info(context.Background(), "⚠️ No buddy nodes found from any source (expected", ion.String("args", fmt.Sprintf("⚠️ No buddy nodes found from any source (expected %d MaxMainPeers)", expectedBuddyCount)))
			logger().Info(context.Background(), "⚠️ Cannot connect to other nodes for CRDT sync")
			return nil
		}
		logger().Info(context.Background(), "📋 Falling back to", ion.String("args", fmt.Sprintf("📋 Falling back to %d buddy peer IDs (will resolve multiaddrs)", len(fallbackIDs))))

		// Convert fallback IDs into targets by resolving multiaddrs below
		for _, pid := range fallbackIDs {
			buddyTargets = append(buddyTargets, AVCStruct.Buddy_PeerMultiaddr{PeerID: pid})
		}
	}

	if len(buddyTargets) < expectedBuddyCount {
		logger().Info(context.Background(), fmt.Sprintf("⚠️ Only found %d buddy nodes, expected %d (config.MaxMainPeers)", len(buddyTargets), expectedBuddyCount))
	}

	logger().Info(context.Background(), fmt.Sprintf("✅ Total buddy nodes to connect: %d (expected: %d from config.MaxMainPeers)", len(buddyTargets), expectedBuddyCount))

	logger().Info(context.Background(), "🔌 Connecting to", ion.String("args", fmt.Sprintf("🔌 Connecting to %d buddy nodes for CRDT sync...", len(buddyTargets))))

	connectedCount := 0
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Connect to each buddy node
	for _, target := range buddyTargets {
		buddyPeerID := target.PeerID
		// Skip self
		if buddyPeerID == listenerNode.PeerID {
			continue
		}

		// Check if already connected
		if listenerNode.Host.Network().Connectedness(buddyPeerID) == network.Connected {
			logger().Info(context.Background(), "✅ Already connected to buddy", ion.String("args", fmt.Sprintf("✅ Already connected to buddy %s", buddyPeerID.String()[:8])))
			connectedCount++
			continue
		}

		var multiaddrs []multiaddr.Multiaddr

		// Priority 1: Use target's provided multiaddr if present
		if target.Multiaddr != nil {
			multiaddrs = []multiaddr.Multiaddr{target.Multiaddr}
			logger().Info(context.Background(), "📋 Using provided multiaddr for buddy", ion.String("args", fmt.Sprintf("📋 Using provided multiaddr for buddy %s: %s", buddyPeerID.String()[:8], target.Multiaddr.String())))
		}

		// Priority 2: Try to get from peerstore (fastest local source)
		if len(multiaddrs) == 0 {
			peerstoreAddrs := listenerNode.Host.Peerstore().Addrs(buddyPeerID)
			if len(peerstoreAddrs) > 0 {
				multiaddrs = peerstoreAddrs
				logger().Info(context.Background(), "📋 Got", ion.String("args", fmt.Sprintf("📋 Got %d multiaddrs from peerstore for buddy %s", len(multiaddrs), buddyPeerID.String()[:8])))
			}
		}

		// Priority 3: Query seed node as last resort
		if len(multiaddrs) == 0 && settings.Get().Network.SeedNode != "" {
			logger().Info(context.Background(), "🔍 Querying seed node for multiaddr of buddy", ion.String("args", fmt.Sprintf("🔍 Querying seed node for multiaddr of buddy %s...", buddyPeerID.String()[:8])))

			client, err := seednode.NewClient(settings.Get().Network.SeedNode)
			if err == nil {
				// Try to get peer record from seed node
				peerRecord, err := client.GetPeer(buddyPeerID.String())
				if err == nil && peerRecord != nil && len(peerRecord.GetMultiaddrs()) > 0 {
					for _, addrStr := range peerRecord.GetMultiaddrs() {
						if maddr, err := multiaddr.NewMultiaddr(addrStr); err == nil {
							multiaddrs = append(multiaddrs, maddr)
						}
					}
					logger().Info(context.Background(), "📋 Got", ion.String("args", fmt.Sprintf("📋 Got %d multiaddrs from seed node for buddy %s", len(multiaddrs), buddyPeerID.String()[:8])))
				} else if err != nil {
					logger().Info(context.Background(), "⚠️ Failed to get peer from seed node:", ion.String("args", fmt.Sprintf("⚠️ Failed to get peer from seed node: %v", err)))
				}
				// Closed explicitly, NOT deferred: this is inside the per-buddy loop,
				// so a defer would hold every connection until the whole sync finished
				// — one per buddy per round. Placed here, after the last use of
				// client, and before the connection-attempt block below whose
				// `goto nextPeer` would otherwise jump past it.
				client.Close()
			} else {
				logger().Info(context.Background(), "⚠️ Failed to create seed node client:", ion.String("args", fmt.Sprintf("⚠️ Failed to create seed node client: %v", err)))
			}
		}

		// Attempt connection
		if len(multiaddrs) > 0 {
			peerInfo := peer.AddrInfo{
				ID:    buddyPeerID,
				Addrs: multiaddrs,
			}

			logger().Info(context.Background(), "🔌 Attempting to connect to buddy", ion.String("args", fmt.Sprintf("🔌 Attempting to connect to buddy %s at %s...", buddyPeerID.String()[:8], multiaddrs[0].String())))

			if err := listenerNode.Host.Connect(ctx, peerInfo); err != nil {
				logger().Info(context.Background(), "❌ Failed to connect to buddy", ion.String("args", fmt.Sprintf("❌ Failed to connect to buddy %s: %v", buddyPeerID.String()[:8], err)))
				// Try next multiaddr if available
				if len(multiaddrs) > 1 {
					for i := 1; i < len(multiaddrs) && i < 3; i++ { // Try up to 3 addresses
						peerInfo.Addrs = []multiaddr.Multiaddr{multiaddrs[i]}
						if err := listenerNode.Host.Connect(ctx, peerInfo); err == nil {
							logger().Info(context.Background(), "✅ Connected to buddy", ion.String("args", fmt.Sprintf("✅ Connected to buddy %s using fallback address", buddyPeerID.String()[:8])))
							connectedCount++
							goto nextPeer
						}
					}
				}
			} else {
				logger().Info(context.Background(), "✅ Connected to buddy", ion.String("args", fmt.Sprintf("✅ Connected to buddy %s", buddyPeerID.String()[:8])))
				connectedCount++
			}
		} else {
			logger().Info(context.Background(), "⚠️ No multiaddrs found for buddy", ion.String("args", fmt.Sprintf("⚠️ No multiaddrs found for buddy %s, skipping connection", buddyPeerID.String()[:8])))
		}

	nextPeer:
		// Small delay between connections
		time.Sleep(100 * time.Millisecond)
	}

	logger().Info(context.Background(), "✅ Connected to", ion.String("args", fmt.Sprintf("✅ Connected to %d/%d buddy nodes for CRDT sync", connectedCount, len(buddyTargets))))

	// Wait a moment for connections to establish
	time.Sleep(1 * time.Second)

	return nil
}

// rawLWWSet is the wire shape both the legacy jmdn CRDT engine and avc's
// share byte-for-byte (verified: crdt.LWWSet in both repos carries identical
// `json:"key"/"adds"/"removes"` tags). One decode target serves both
// keyspaces — only the ELEMENT strings inside Adds differ in meaning between
// them, never the envelope.
type rawLWWSet struct {
	Key     string                 `json:"key"`
	Adds    map[string]interface{} `json:"adds"`
	Removes map[string]interface{} `json:"removes"`
}

// buildLocalSyncData gathers this node's outgoing CRDT state for one sync
// round, from both engines. Stage 3
// (docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md) — extracted from
// TriggerCRDTSyncForBuddyNode so the two-engine union has one place to
// change and one place to test, rather than living inline in a 380-line
// function.
//
// Combining both engines into a single map is safe, not just convenient:
// votes.OwnsKey's own guarantee is that a block-keyed key can never collide
// with a legacy peer-ID key, so there is no ambiguity for the receiver to
// resolve later.
func buildLocalSyncData(listenerNode *AVCStruct.BuddyNode) map[string]json.RawMessage {
	syncData := make(map[string]json.RawMessage)

	addAll := func(all map[string]crdt.CRDT) {
		for key, obj := range all {
			data, err := json.Marshal(obj)
			if err != nil {
				logger().Info(context.Background(), "⚠️ Failed to marshal CRDT for key",
					ion.String("args", fmt.Sprintf("⚠️ Failed to marshal CRDT for key %s: %v", key, err)))
				continue
			}
			syncData[key] = data
		}
	}

	if listenerNode.CRDTLayer != nil && listenerNode.CRDTLayer.CRDTLayer != nil {
		addAll(listenerNode.CRDTLayer.CRDTLayer.GetAllCRDTs())
	}
	// VoteCRDTLayer is nil-guarded, not required: a node mid-migration (or
	// with JMDN_VOTE_CRDT_V2 never having written anything yet) still
	// publishes its legacy state exactly as before this stage.
	if listenerNode.VoteCRDTLayer != nil && listenerNode.VoteCRDTLayer.CRDTLayer != nil {
		for key, obj := range listenerNode.VoteCRDTLayer.CRDTLayer.GetAllCRDTs() {
			data, err := json.Marshal(obj)
			if err != nil {
				logger().Info(context.Background(), "⚠️ Failed to marshal v2 vote CRDT for key",
					ion.String("args", fmt.Sprintf("⚠️ Failed to marshal v2 vote CRDT for key %s: %v", key, err)))
				continue
			}
			syncData[key] = data
		}
	}

	return syncData
}

// mergeCRDTData merges received CRDT data into the local CRDT layer(s).
//
// Two keyspaces travel in one sync message as of Stage 3
// (docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md §4): the legacy scheme, keyed by the
// voting peer's ID, and the new block-keyed scheme
// (votes:<height>:<blockHash> / votesig:<height>:<blockHash>) written by
// avc/crdt/votes.AddVote. Every key is routed by votes.OwnsKey before
// anything else happens to it — never by trying to decode it as a peer ID
// first, which is what would make the two keyspaces ambiguous instead of
// merely different.
func mergeCRDTData(listenerNode *AVCStruct.BuddyNode, syncMsg CRDTSync.Message) error {
	if listenerNode.CRDTLayer == nil || listenerNode.CRDTLayer.CRDTLayer == nil {
		return fmt.Errorf("CRDT layer not available")
	}

	// Get the sender's peer ID (who sent this sync message)
	senderPeerID, err := peer.Decode(syncMsg.NodeID)
	if err != nil {
		return fmt.Errorf("invalid sender peer ID: %w", err)
	}

	logger().Info(context.Background(), "🔄 Merging CRDT data from peer", ion.String("args", fmt.Sprintf("🔄 Merging CRDT data from peer %s", senderPeerID.String()[:8])))

	legacyMerged, voteMerged := 0, 0
	for key, rawData := range syncMsg.SyncData {
		if avcvotes.OwnsKey(key) {
			n, err := mergeVoteCRDTElement(listenerNode, senderPeerID, key, rawData)
			if err != nil {
				logger().Info(context.Background(), "⚠️ Failed to merge v2 vote CRDT for key",
					ion.String("args", fmt.Sprintf("⚠️ Failed to merge v2 vote CRDT for key %s: %v", key, err)))
				continue
			}
			voteMerged += n
			continue
		}

		n, err := mergeLegacyVoteElement(listenerNode, key, rawData)
		if err != nil {
			logger().Info(context.Background(), "⚠️ Failed to merge legacy CRDT for key",
				ion.String("args", fmt.Sprintf("⚠️ Failed to merge legacy CRDT for key %s: %v", key, err)))
			continue
		}
		legacyMerged += n
	}

	logger().Info(context.Background(), "✅ Completed merging CRDT data from peer",
		ion.String("args", fmt.Sprintf("✅ Completed merging CRDT data from peer %s (%d legacy, %d v2 elements)",
			senderPeerID.String()[:8], legacyMerged, voteMerged)))

	return nil
}

// mergeLegacyVoteElement applies one remote CRDT object's elements into the
// legacy engine, keyed by the voting peer's own ID — unchanged behavior from
// before Stage 3, just factored out of mergeCRDTData's loop body.
func mergeLegacyVoteElement(listenerNode *AVCStruct.BuddyNode, votePeerIDStr string, rawData json.RawMessage) (merged int, err error) {
	votePeerID, err := peer.Decode(votePeerIDStr)
	if err != nil {
		return 0, fmt.Errorf("invalid peer ID in sync data: %w", err)
	}

	var remoteCRDT rawLWWSet
	if err := json.Unmarshal(rawData, &remoteCRDT); err != nil {
		return 0, fmt.Errorf("unmarshaling CRDT: %w", err)
	}

	for element := range remoteCRDT.Adds {
		if err := DataLayer.Add(listenerNode.CRDTLayer, votePeerID, votePeerIDStr, element); err != nil {
			logger().Info(context.Background(), "⚠️ Failed to add vote element to CRDT for peer",
				ion.String("args", fmt.Sprintf("⚠️ Failed to add vote element to CRDT for peer %s: %v", votePeerIDStr[:8], err)))
			continue
		}
		merged++
	}
	return merged, nil
}

// mergeVoteCRDTElement applies one remote block-keyed CRDT object's elements
// into the v2 (avc) engine. Written via avc/buddynodes/datalayer.Add — the
// same low-level primitive AddVote itself writes through — rather than
// re-parsing elements back into a votes.VoteRecord and re-calling AddVote:
// a votes: element is only "<peerID>:<vote>", with height/blockHash implicit
// in the KEY, not recoverable from the element alone, so AddVote's own
// signature does not fit a merge. This mirrors mergeLegacyVoteElement's own
// shape exactly, just against the other engine.
//
// senderPeerID is attributed as the writing actor for this merge — the same
// role votePeerID plays in the legacy path — not the original voter, which
// is already encoded in the element string itself and is what
// votes.TallyBlock authenticates against later.
//
// A8-1 (docs/VALIDATOR-SCALE-VOTE-AGGREGATION-LLD.md): applies the same two
// guards AddVote enforces on its own write path — the compaction watermark
// and the per-peer ingest cap — which this merge path bypassed by writing
// through avcdatalayer.Add directly instead of through AddVote. That bypass
// predates Stage 6 (avc/crdt/votes.DefaultWatermark.ConvergeAndCompact,
// wired live via vote_crdt_compaction.go), when the watermark genuinely
// never advanced past 0 and the check really was a no-op; Stage 6 has since
// landed, so a lagging or hostile peer's sync data can now resurrect votes
// for a height ConvergeAndCompact already evaluated and deleted, and can
// flood one peer's element count on a key with no bound. AddVote's own
// signature still does not fit here (a votes:/votesig: element carries no
// height of its own — it's implicit in the key — and CRDT sync delivers the
// two keys as independent objects, not matched VoteRecord pairs), so the
// checks are replicated at the same key/element granularity instead of
// routing through AddVote itself.
func mergeVoteCRDTElement(listenerNode *AVCStruct.BuddyNode, senderPeerID peer.ID, key string, rawData json.RawMessage) (merged int, err error) {
	if listenerNode.VoteCRDTLayer == nil || listenerNode.VoteCRDTLayer.CRDTLayer == nil {
		return 0, fmt.Errorf("v2 vote CRDT layer not available on this node")
	}

	if height, ok := avcvotes.HeightFromKey(key); ok && height <= avcvotes.DefaultWatermark.Current() {
		// Expected, not a bug: a lagging peer replaying sync data for a
		// height already converged and compacted. Same "discard at the
		// write boundary" reasoning as AddVote's own ErrHeightCompacted.
		return 0, nil
	}

	var remoteCRDT rawLWWSet
	if err := json.Unmarshal(rawData, &remoteCRDT); err != nil {
		return 0, fmt.Errorf("unmarshaling v2 CRDT: %w", err)
	}

	for element := range remoteCRDT.Adds {
		peerID, _, found := strings.Cut(element, ":")
		if found && avcvotes.CountElementsForPeer(listenerNode.VoteCRDTLayer, key, peerID) >= avcvotes.MaxElementsPerPeerPerBlock {
			// Ingest cap reached for this peer on this key — same bound
			// AddVote enforces, applied here since a merge can inject many
			// elements at once, unlike AddVote's one-call-one-vote shape.
			continue
		}
		if err := avcdatalayer.Add(listenerNode.VoteCRDTLayer, senderPeerID, key, element); err != nil {
			logger().Info(context.Background(), "⚠️ Failed to add v2 vote element for key",
				ion.String("args", fmt.Sprintf("⚠️ Failed to add v2 vote element for key %s: %v", key, err)))
			continue
		}
		merged++
	}
	return merged, nil
}
