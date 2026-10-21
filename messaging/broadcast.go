package messaging

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/Vote"
	"gossipnode/config"
	"gossipnode/config/GRO"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"
	"gossipnode/explorer/lifecycle"
	"gossipnode/internal/l1auth"
	"gossipnode/internal/proposalguard"
	"gossipnode/messaging/BlockProcessing"
	GROHelper "gossipnode/messaging/common"
	"gossipnode/metrics"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// BroadcastMessage represents a message that is broadcast through the network
type BroadcastMessageStruct struct {
	ID        string `json:"id"`        // Unique message ID
	Sender    string `json:"sender"`    // Original sender's peer ID
	Content   string `json:"content"`   // Message content
	Timestamp int64  `json:"timestamp"` // Unix timestamp when message was created
	Hops      int    `json:"hops"`      // How many hops this message has made
	Type      string `json:"type"`      // Message type: "general", "vote_trigger", etc.
	Data      string `json:"data"`      // Additional data for specific message types
}

// Track seen messages to prevent loops
// seenMessages is the broadcast dedup cache: message ID -> seen. Bounded in
// both size and age: entries expire after config.MessageExpiryTime, and at
// most maxSeenMessages are held (least recently added evicted first), so a
// flood of unique IDs cannot grow memory without limit.
const maxSeenMessages = 100_000

var seenMessages = expirable.NewLRU[string, struct{}](maxSeenMessages, nil, config.MessageExpiryTime)

func generateMessageID(sender, content string, timestamp int64) string {
	hasher := sha256.New()
	hasher.Write(fmt.Appendf(nil, "%s-%s-%d", sender, content, timestamp))
	hash := base64.URLEncoding.EncodeToString(hasher.Sum(nil))
	return hash[:16] // Return first 16 chars for brevity
}

// StartBroadcastCleanup is kept for callers; expiry is now handled by the
// bounded cache itself, so there is no background loop to start.
func StartBroadcastCleanup() {}

func isMessageSeen(msgID string) bool {
	return seenMessages.Contains(msgID)
}

// markMessageSeen records that we've seen this message
func markMessageSeen(msgID string) {
	seenMessages.Add(msgID, struct{}{})
}

// HandleBroadcastStream processes incoming broadcast messages
func HandleBroadcastStream(stream network.Stream) {
	defer stream.Close()

	// Record metrics
	metrics.MessagesReceivedCounter.WithLabelValues("broadcast", metrics.PeerLabel(stream.Conn().RemotePeer().String())).Inc()

	// Bound the read: cap the size (remote-OOM guard) and set a deadline so a
	// slow/idle peer cannot hold the stream open forever. Mirrors the bounded
	// direct block stream in blockPropagation.go (shared package constants).
	_ = stream.SetReadDeadline(time.Now().Add(blockStreamReadTimeout))
	reader := bufio.NewReader(io.LimitReader(stream, maxBlockStreamBytes))
	messageBytes, err := reader.ReadBytes('\n')
	if err != nil {
		if err != io.EOF {
			broadcastLogger().Error(context.Background(), "Error reading broadcast message", err,
				ion.String("peer", stream.Conn().RemotePeer().String()))
		}
		return
	}

	// Parse the message
	var msg BroadcastMessageStruct
	if err := json.Unmarshal(messageBytes, &msg); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to unmarshal broadcast message", err)
		return
	}

	// Envelope fields are sender-chosen; validate them before they drive dedup
	// or relaying.
	if reason := checkBroadcastEnvelope(&msg, stream.Conn().RemotePeer().String(), time.Now().UTC()); reason != "" {
		broadcastLogger().Warn(context.Background(), "Dropping broadcast message",
			ion.String("reason", reason),
			ion.String("peer", stream.Conn().RemotePeer().String()),
			ion.String("type", msg.Type))
		return
	}

	// Check if we've already seen this message
	if isMessageSeen(msg.ID) {
		// We've already processed this message, ignore
		return
	}

	// Mark as seen to avoid reprocessing
	markMessageSeen(msg.ID)

	broadcastLogger().Info(context.Background(), "Broadcast received",
		ion.String("sender", msg.Sender),
		ion.String("content", msg.Content))

	// Handle different message types
	if msg.Type == voteTriggerBroadcastType {
		handleVoteTriggerBroadcast(msg, stream.Conn().RemotePeer())
	}

	// Only rebroadcast if we haven't reached max hops. A vote_trigger is NEVER
	// re-flooded: it carries the full proposed block (2+ MB with the proof) and the
	// sequencer already delivers it directly to every committee member, so every
	// re-flooded copy was being dropped by isMessageSeen on arrival AFTER the
	// bytes crossed the wire (observed on testnet: 15 validators × 14 peers ≈ 560 MB
	// of pure duplicates per block). The sender also sets Hops=MaxHops on these
	// so that receivers running an older binary skip the re-flood too.
	// Only message types that need network-wide delivery are relayed. Anything
	// else (vote triggers, plain operator messages, unknown types) is consumed
	// by the direct recipient only, so a peer cannot use this node to flood
	// arbitrary payloads.
	if !broadcastRelayable(msg.Type) {
		return
	}
	if msg.Hops < config.MaxHops {
		// Forward to our peers
		msg.Hops++
		localPeer := stream.Conn().LocalPeer().String()
		broadcastLogger().Info(context.Background(), "Rebroadcasting message",
			ion.String("msg_id", msg.ID),
			ion.String("origin", msg.Sender),
			ion.String("via", localPeer),
			ion.Int("hops", msg.Hops))

		// Instead of trying to get the host from the connection,
		// get it from the stored node instance
		// We'll need to access the global node instance
		if hostInstance := getHostInstance(); hostInstance != nil {
			forwardBroadcast(hostInstance, msg)
		} else {
			broadcastLogger().Error(context.Background(), "Cannot access host instance for forwarding broadcast",
				errors.New("getHostInstance returned nil"))
		}
	} else {
		broadcastLogger().Info(context.Background(), "Max hops reached, not rebroadcasting",
			ion.String("msg_id", msg.ID),
			ion.Int("hops", msg.Hops))
	}
}

// Accepted clock window for a broadcast's Timestamp. The past bound sits one
// cleanup interval inside MessageExpiryTime, so a message is rejected as stale
// before its ID can have been evicted from the seen-cache and replayed.
const (
	broadcastMaxAge    = config.MessageExpiryTime - time.Minute
	broadcastMaxFuture = time.Minute
)

// relayableBroadcastTypes are the message types this node re-floods to its
// peers. Add a type here only when it genuinely needs network-wide delivery.
//
// The set is empty. It held the three timeout-certificate types (vote,
// request, cert) when this check was written; 94e8fb6 (#196) removed that flow
// and its constants with it. vote_trigger is excluded deliberately and
// separately: it carries the full proposed block, the sequencer already
// delivers it directly to every committee member, and re-flooding it cost
// about 560 MB of duplicate traffic per block on testnet.
//
// An empty set is the intended state, not an oversight. Before this check,
// any unrecognised type was re-flooded to every connected peer, so one peer
// could use this node to spread an arbitrary payload across the network.
var relayableBroadcastTypes = map[string]bool{}

// broadcastRelayable reports whether a received message type is re-flooded.
func broadcastRelayable(msgType string) bool {
	return relayableBroadcastTypes[msgType]
}

// expectedBroadcastID recomputes the message ID the way each sender builds it.
// Vote triggers hash Content only; every other sender hashes Content+Data.
func expectedBroadcastID(msg *BroadcastMessageStruct) string {
	if msg.Type == voteTriggerBroadcastType {
		return generateMessageID(msg.Sender, msg.Content, msg.Timestamp)
	}
	return generateMessageID(msg.Sender, msg.Content+msg.Data, msg.Timestamp)
}

// checkBroadcastEnvelope validates the sender-controlled envelope fields.
// Returns "" when the message may be processed, otherwise a short reason.
func checkBroadcastEnvelope(msg *BroadcastMessageStruct, remotePeer string, now time.Time) string {
	if msg.Hops < 0 || msg.Hops > config.MaxHops {
		return "hops_out_of_range"
	}
	ts := time.Unix(msg.Timestamp, 0)
	if now.Sub(ts) > broadcastMaxAge {
		return "stale"
	}
	if ts.Sub(now) > broadcastMaxFuture {
		return "future_timestamp"
	}
	// The ID is the dedup key. Binding it to the content stops a peer from
	// re-sending one payload under fresh IDs to get it processed and relayed
	// again.
	if msg.ID != expectedBroadcastID(msg) {
		return "id_mismatch"
	}
	// An un-relayed message must come from its stated origin.
	if msg.Hops == 0 && msg.Sender != remotePeer {
		return "sender_mismatch"
	}
	return ""
}

// Store a reference to the host for broadcast handling
var (
	hostInstance host.Host
	hostMutex    sync.RWMutex
)

// SetHostInstance stores the host instance for broadcast use
func SetHostInstance(h host.Host) {
	hostMutex.Lock()
	defer hostMutex.Unlock()
	hostInstance = h
}

// getHostInstance safely retrieves the stored host instance
func getHostInstance() host.Host {
	hostMutex.RLock()
	defer hostMutex.RUnlock()
	return hostInstance
}

// forwardBroadcast sends the message to all connected peers
func forwardBroadcast(h host.Host, msg BroadcastMessageStruct) {
	// Get all connected peers
	peers := h.Network().Peers()

	// Convert message to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to marshal broadcast message", err)
		return
	}
	msgBytes = append(msgBytes, '\n')

	// Track how many peers we successfully broadcasted to
	var successCount int

	// Send to each peer (except original sender)
	for _, peerID := range peers {
		// Don't send back to the original sender
		if peerID.String() == msg.Sender {
			continue
		}

		// Open a stream to the peer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stream, err := h.NewStream(ctx, peerID, config.BroadcastProtocol)
		cancel()

		if err != nil {
			broadcastLogger().Warn(context.Background(), "Failed to open broadcast stream",
				ion.Err(err),
				ion.String("peer", peerID.String()))
			continue
		}

		// Write the message (bounded: a peer that stops reading must not wedge us)
		_ = stream.SetWriteDeadline(time.Now().Add(broadcastWriteTimeout))
		_, err = stream.Write(msgBytes)
		if err != nil {
			broadcastLogger().Warn(context.Background(), "Failed to write broadcast message",
				ion.Err(err),
				ion.String("peer", peerID.String()))
			_ = stream.Reset()
			continue
		}

		// Close the stream
		stream.Close()
		successCount++

		// Record metrics
		metrics.MessagesSentCounter.WithLabelValues("broadcast", metrics.PeerLabel(peerID.String())).Inc()
	}

	broadcastLogger().Info(context.Background(), "Broadcast forwarded to peers",
		ion.String("msg_id", msg.ID),
		ion.Int("peers", successCount))
}

// BroadcastMessage sends a message to all connected peers
func BroadcastMessage(h host.Host, content string) error {
	if BroadcastLocalGRO == nil {
		var err error
		BroadcastLocalGRO, err = GROHelper.InitializeGRO(GRO.BroadcastLocal)
		if err != nil {
			broadcastLogger().Error(context.Background(), "Failed to initialize BroadcastLocalGRO", err)
			return err
		}
	}
	// Create a new broadcast message
	now := time.Now().UTC().Unix()
	msg := BroadcastMessageStruct{
		Sender:    h.ID().String(),
		Content:   content,
		Timestamp: now,
		Hops:      0,
	}

	// Generate a unique ID based on content and timestamp
	msg.ID = generateMessageID(msg.Sender, content, now)

	// Remember this message so we don't process it if we receive it back
	markMessageSeen(msg.ID)

	// Convert to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal broadcast message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	// Get all connected peers
	peers := h.Network().Peers()
	if len(peers) == 0 {
		return fmt.Errorf("no connected peers to broadcast to")
	}

	broadcastLogger().Info(context.Background(), "Starting broadcast to peers",
		ion.String("msg_id", msg.ID),
		ion.Int("peers", len(peers)))

	// Send message to all peers
	wg, err := BroadcastLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.BroadcastForwardWG)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to create waitgroup for broadcast forwarding", err)
		return fmt.Errorf("failed to create waitgroup for broadcast forwarding: %w", err)
	}
	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		BroadcastLocalGRO.Go(GRO.BroadcastForwardThread, func(ctx context.Context) error {
			peer := peerID

			// Open stream to peer with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := h.NewStream(ctx, peer, config.BroadcastProtocol)
			if err != nil {
				broadcastLogger().Warn(context.Background(), "Failed to open broadcast stream",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}
			defer stream.Close()

			// Send the message (bounded: a peer that stops reading must not wedge us)
			_ = stream.SetWriteDeadline(time.Now().Add(broadcastWriteTimeout))
			_, err = stream.Write(msgBytes)
			if err != nil {
				_ = stream.Reset()
				broadcastLogger().Warn(context.Background(), "Failed to send broadcast message",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}

			// Record success
			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			// Record metrics
			metrics.MessagesSentCounter.WithLabelValues("broadcast", metrics.PeerLabel(peer.String())).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.BroadcastForwardWG))
	}

	// Wait for all sends to complete
	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("failed to broadcast message to any peers")
	}

	broadcastLogger().Info(context.Background(), "Broadcast complete",
		ion.String("msg_id", msg.ID),
		ion.Int("success", successCount),
		ion.Int("total", len(peers)))

	return nil
}

// handleVoteTriggerBroadcast processes vote trigger broadcast messages
func handleVoteTriggerBroadcast(msg BroadcastMessageStruct, from peer.ID) {
	broadcastLogger().Info(context.Background(), "Processing vote trigger broadcast",
		ion.String("msg_id", msg.ID),
		ion.String("sender", msg.Sender),
		ion.String("from", from.String()),
		ion.String("type", msg.Type))

	// Only the pinned sequencer may ask this node to vote. `from` is the
	// libp2p-authenticated stream peer (Noise/TLS handshake), not the
	// self-declared msg.Sender, and vote triggers are delivered directly by the
	// sequencer (never relayed), so this is a cryptographic sender check.
	// Checked before anything is parsed or cached.
	if ok, enforced := voteTriggerSenderAuthorized(from); !ok {
		metrics.BlocksRejectedCounter.WithLabelValues("vote_trigger_unauthorized_sender", "remote").Inc()
		broadcastLogger().Warn(context.Background(), "Dropping vote trigger from a peer that is not the pinned sequencer",
			ion.String("from", from.String()), ion.String("msg_id", msg.ID))
		return
	} else if !enforced {
		broadcastLogger().Warn(context.Background(), "Vote trigger sender NOT authenticated: consensus.sequencer_pinned_peer_id is unset (any peer can request a vote)",
			ion.String("from", from.String()), ion.String("msg_id", msg.ID))
	}

	// Parse the consensus message data
	var consensusMessage PubSubMessages.ConsensusMessage
	if err := json.Unmarshal([]byte(msg.Data), &consensusMessage); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to unmarshal consensus message from vote trigger", err,
			ion.String("msg_id", msg.ID))
		return
	}

	// Validate the proposal's shape (block present, hash set, every transaction
	// has a sender) before anything uses or caches it.
	if err := validateVoteTriggerConsensusMessage(&consensusMessage); err != nil {
		metrics.BlocksRejectedCounter.WithLabelValues("vote_trigger_malformed", "remote").Inc()
		broadcastLogger().Warn(context.Background(), "Dropping malformed vote trigger",
			ion.Err(err), ion.String("from", from.String()), ion.String("msg_id", msg.ID))
		return
	}

	// Check if ForListner is initialized
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		broadcastLogger().Error(context.Background(), "ForListner not initialized - cannot submit vote",
			errors.New("ForListner is nil"),
			ion.String("msg_id", msg.ID))
		return
	}

	broadcastLogger().Debug(context.Background(), "ForListner initialized",
		ion.String("node_id", listenerNode.PeerID.String()))

	// IMPORTANT: Populate buddy nodes list from consensus message
	// Extract ONLY the final connected buddy nodes (MaxMainPeers) from consensus message's Buddies map
	// These are the buddy nodes selected by sequencer (main candidates + backup fill-ins if needed)
	buddyPeerIDs := make([]peer.ID, 0, config.MaxMainPeers)
	buddiesMap := consensusMessage.GetBuddies()
	if len(buddiesMap) > 0 {
		// Buddies is a map[int]Buddy_PeerMultiaddr
		// The consensus message contains exactly MaxMainPeers buddies (the final connected ones)
		// Extract them in order (by map key index 0, 1, 2, ... MaxMainPeers-1)
		for i := 0; i < config.MaxMainPeers && i < len(buddiesMap); i++ {
			if buddy, exists := buddiesMap[i]; exists {
				// Check if PeerID is valid (not empty)
				if buddy.PeerID != "" && len(buddy.PeerID.String()) > 0 {
					buddyPeerIDs = append(buddyPeerIDs, buddy.PeerID)
				}
			}
		}
	}

	// Populate listener node's buddy list with the final connected buddy nodes (MaxMainPeers)
	if len(buddyPeerIDs) > 0 {
		listenerNode.BuddyNodes.Buddies_Nodes = buddyPeerIDs
		broadcastLogger().Info(context.Background(), "Populated buddy nodes list from consensus message",
			ion.Int("buddy_count", len(buddyPeerIDs)),
			ion.Int("max_main_peers", config.MaxMainPeers))
	} else {
		broadcastLogger().Warn(context.Background(), "No buddy nodes found in consensus message - CRDT sync may fail",
			ion.String("msg_id", msg.ID))
	}

	// Store consensus message in global cache (needed for CRDT sync multiaddr lookup)
	consensusMessage.SetGloalVarCacheConsensusMessage()
	broadcastLogger().Debug(context.Background(), "Stored consensus message in cache for multiaddr lookup",
		ion.String("msg_id", msg.ID))

	// Create vote trigger and submit vote
	voteTrigger := Vote.NewVoteTrigger()
	voteTrigger.SetConsensusMessage(&consensusMessage)

	broadcastLogger().Debug(context.Background(), "Submitting vote",
		ion.String("msg_id", msg.ID))

	// Submit the vote (this will send Type_SubmitVote message via SubmitMessageProtocol)
	if err := voteTrigger.SubmitVote(); err != nil {
		if !errors.Is(err, Vote.ErrGossipPublishOnly) {
			broadcastLogger().Error(context.Background(), "Failed to submit vote from broadcast trigger", err,
				ion.String("msg_id", msg.ID))
			return
		}
		// Partial success, not failure: the vote IS out via direct-send. Peers
		// that missed the gossip publish pick it up on the next CRDT sync
		// before tally, so this must not be logged as a failed submission.
		broadcastLogger().Warn(context.Background(), "Vote delivered via direct-send only — gossip publish failed",
			ion.Err(err),
			ion.String("msg_id", msg.ID))
	}

	broadcastLogger().Info(context.Background(), "Vote submitted - waiting for consensus confirmation broadcast",
		ion.String("msg_id", msg.ID),
		ion.String("block_hash", consensusMessage.GetZKBlock().BlockHash.Hex()))
}

const voteTriggerBroadcastType = "vote_trigger"

// voteTriggerPinnedSequencer returns the configured sequencer pin. A variable
// so tests can set it without loading the settings file.
var voteTriggerPinnedSequencer = func() string {
	if !settings.IsLoaded() {
		return ""
	}
	return settings.Get().Consensus.SequencerPinnedPeerID
}

// voteTriggerSenderAuthorized reports whether a vote trigger from the
// transport-authenticated peer `from` may be acted on. Same contract as
// l1finality.AuthorizeGossipSender: with consensus.sequencer_pinned_peer_id set
// (required under production posture) only that peer is accepted
// (enforced=true); with it unset, every sender is accepted and the caller must
// WARN (enforced=false) — the pre-pin testnet behaviour.
func voteTriggerSenderAuthorized(from peer.ID) (ok, enforced bool) {
	pin := voteTriggerPinnedSequencer()
	if !l1auth.Enforced(pin) {
		return true, false
	}
	return l1auth.IsSequencer(from.String(), pin), true
}

// validateVoteTriggerConsensusMessage rejects a vote trigger whose proposal the
// vote path would dereference unsafely: no block, a zero block hash, or a
// transaction with no sender.
func validateVoteTriggerConsensusMessage(cm *PubSubMessages.ConsensusMessage) error {
	if cm == nil {
		return errors.New("nil consensus message")
	}
	zk := cm.GetZKBlock()
	if zk == nil {
		return errors.New("vote trigger carries no block")
	}
	if zk.BlockHash == (common.Hash{}) {
		return errors.New("vote trigger block has no hash")
	}
	for i := range zk.Transactions {
		if err := Security.CheckTransactionShape(&zk.Transactions[i]); err != nil {
			return fmt.Errorf("transaction %d: %w", i, err)
		}
	}
	return nil
}

// voteTriggerDialTimeout bounds the dial to a committee member we are not yet
// connected to. Short: the round has ConsensusTimeout to complete and a member
// that cannot be reached in a few seconds is treated like a non-responder.
const voteTriggerDialTimeout = 5 * time.Second

// voteTriggerVoters resolves the validator VOTER set for a height — every
// snapshot-registered, BLS-keyed validator whose vote a buddy counts at tally
// (AuthorizedVotersForTallyAtHeight). A variable so tests can inject a set.
var voteTriggerVoters = AuthorizedVotersForTallyAtHeight

// voteTriggerTargets returns the peers that must receive the proposal.
//
// CONSENSUS DESIGN: every eligible validator VOTES; the seated committee
// (ConsensusMessage.Buddies) AGGREGATES — each buddy tallies all validator votes
// it holds and returns its own BLS-signed result, and the sequencer certifies
// 2f+1 of those. So the trigger must reach the whole voter set, not only the
// committee. (#197 narrowed it to the committee, which silently stopped every
// non-seated validator from voting.)
//
// Targets = Buddies ∪ voter set for the block's height, self excluded, each
// sent ONE copy: the trigger is never re-flooded (Hops = MaxHops, and receivers
// return before forwardBroadcast), so a voter the sequencer cannot reach is
// dialled here rather than relayed through other peers. Buddies are dialled
// from the multiaddr they carry; other voters from the peerstore.
//
// If the voter set cannot be resolved, the trigger goes to every connected peer
// plus the committee — the pre-#197 reach without the re-flood. Delivery is
// fail-open on purpose: who is COUNTED is enforced at tally time
// (authorizedVotersFor), not by who receives the trigger.
func voteTriggerTargets(h host.Host, cm *PubSubMessages.ConsensusMessage) ([]peer.ID, string) {
	self := h.ID()
	type cand struct {
		id    peer.ID
		addrs []multiaddr.Multiaddr
	}
	seen := make(map[peer.ID]struct{})
	var cands []cand
	add := func(pid peer.ID, addrs []multiaddr.Multiaddr) {
		if pid == "" || pid == self {
			return
		}
		if _, dup := seen[pid]; dup {
			return
		}
		seen[pid] = struct{}{}
		cands = append(cands, cand{id: pid, addrs: addrs})
	}

	for _, b := range cm.GetBuddies() {
		var addrs []multiaddr.Multiaddr
		if b.Multiaddr != nil {
			addrs = []multiaddr.Multiaddr{b.Multiaddr}
		}
		add(b.PeerID, addrs)
	}

	scope := "committee+voters"
	var voters map[string]string
	var verr error
	if zk := cm.GetZKBlock(); zk != nil {
		voters, verr = voteTriggerVoters(zk.BlockNumber)
	} else {
		verr = fmt.Errorf("consensus message carries no block")
	}
	if verr != nil || len(voters) == 0 {
		scope = "all-peers(no-voter-set)"
		broadcastLogger().Warn(context.Background(), "Vote trigger: voter set unavailable — sending to every connected peer and the committee",
			ion.Err(verr), ion.Int("voters", len(voters)))
		for _, p := range h.Network().Peers() {
			add(p, nil)
		}
	} else {
		for id := range voters {
			pid, err := peer.Decode(id)
			if err != nil {
				continue
			}
			add(pid, h.Peerstore().Addrs(pid))
		}
	}

	// Dial the unconnected candidates concurrently; keep only reachable ones.
	var (
		mu          sync.Mutex
		wg          sync.WaitGroup
		targets     = make([]peer.ID, 0, len(cands))
		unreachable int
	)
	for _, c := range cands {
		if h.Network().Connectedness(c.id) == network.Connected {
			targets = append(targets, c.id)
			continue
		}
		wg.Add(1)
		go func(c cand) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), voteTriggerDialTimeout)
			err := h.Connect(ctx, peer.AddrInfo{ID: c.id, Addrs: c.addrs})
			cancel()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unreachable++
				broadcastLogger().Warn(context.Background(), "Voter not reachable for vote trigger",
					ion.Err(err), ion.String("peer", c.id.String()))
				return
			}
			targets = append(targets, c.id)
		}(c)
	}
	wg.Wait()
	if unreachable > 0 {
		scope = fmt.Sprintf("%s(unreachable=%d)", scope, unreachable)
	}
	return targets, scope
}

// BroadcastVoteTrigger sends the vote trigger (the full proposal) to every
// eligible validator and the round's committee — see voteTriggerTargets.
func BroadcastVoteTrigger(h host.Host, consensusMessage *PubSubMessages.ConsensusMessage) error {
	if BroadcastLocalGRO == nil {
		var err error
		BroadcastLocalGRO, err = GROHelper.InitializeGRO(GRO.BroadcastLocal)
		if err != nil {
			broadcastLogger().Error(context.Background(), "Failed to initialize BroadcastLocalGRO", err)
			return err
		}
	}
	if consensusMessage == nil {
		return fmt.Errorf("consensus message cannot be nil")
	}

	if consensusMessage.GetZKBlock().BlockHash.String() == "" {
		return fmt.Errorf("consensus message ZKBlock block hash is empty")
	}

	broadcastLogger().Debug(context.Background(), "BroadcastVoteTrigger called",
		ion.String("block_hash", consensusMessage.GetZKBlock().BlockHash.Hex()))

	// Set the voting timer when broadcast starts
	now := time.Now().UTC()
	consensusMessage.SetStartTime(now)
	consensusMessage.SetEndTimeout(now.Add(config.ConsensusTimeout))

	broadcastLogger().Info(context.Background(), "Voting timer set - broadcast vote trigger started",
		ion.String("start_time", now.Format(time.RFC3339)),
		ion.String("end_time", now.Add(config.ConsensusTimeout).Format(time.RFC3339)),
		ion.Duration("timeout_duration", config.ConsensusTimeout))

	// Marshal the consensus message to JSON
	consensusData, err := json.Marshal(consensusMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal consensus message: %w", err)
	}

	// Create a vote trigger broadcast message
	msg := BroadcastMessageStruct{
		Sender:    h.ID().String(),
		Content:   "Vote trigger broadcast - initiate voting process",
		Timestamp: now.Unix(),
		// Hops=MaxHops: receivers (including ones on an older binary, whose only
		// check is Hops < MaxHops) must not re-flood a block-bearing message.
		Hops: config.MaxHops,
		Type: voteTriggerBroadcastType,
		Data: string(consensusData),
	}

	// Generate a unique ID based on content and timestamp
	msg.ID = generateMessageID(msg.Sender, msg.Content, now.Unix())
	broadcastLogger().Debug(context.Background(), "Vote trigger broadcast message prepared",
		ion.String("msg_id", msg.ID),
		ion.String("sender", msg.Sender))

	// Remember this message so we don't process it if we receive it back
	markMessageSeen(msg.ID)

	// Convert to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal vote trigger broadcast message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	// Target every eligible validator (voters) plus the committee (aggregators),
	// one copy each, never re-flooded. Non-validator peers get the FINALIZED
	// block over gossip after 2f+1.
	peers, scope := voteTriggerTargets(h, consensusMessage)
	if len(peers) == 0 {
		return fmt.Errorf("no connected peers to broadcast vote trigger to")
	}

	broadcastLogger().Info(context.Background(), "Starting vote trigger broadcast to peers",
		ion.String("msg_id", msg.ID),
		ion.String("scope", scope),
		ion.Int("peers", len(peers)))

	// Send message to all peers
	wg, err := BroadcastLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.BroadcastVoteTriggerWG)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to create waitgroup for broadcast vote trigger", err)
		return fmt.Errorf("failed to create waitgroup for broadcast vote trigger: %w", err)
	}
	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		BroadcastLocalGRO.Go(GRO.BroadcastVoteTriggerThread, func(ctx context.Context) error {
			peer := peerID
			// Open stream to peer with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := h.NewStream(ctx, peer, config.BroadcastProtocol)
			if err != nil {
				broadcastLogger().Warn(context.Background(), "Failed to open broadcast stream for vote trigger",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}
			defer stream.Close()

			// Send the message. Bounded write: the sequencer's consensus thread
			// blocks on this WaitGroup, so one peer whose yamux receive window
			// is full (stopped reading, half-dead connection) must not stall the
			// whole round. Observed on testnet 2026-10-13: Write parked 6+ min.
			_ = stream.SetWriteDeadline(time.Now().Add(broadcastWriteTimeout))
			_, err = stream.Write(msgBytes)
			if err != nil {
				_ = stream.Reset()
				broadcastLogger().Warn(context.Background(), "Failed to send vote trigger broadcast message",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}

			// Record success
			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			// Record metrics
			metrics.MessagesSentCounter.WithLabelValues("broadcast", metrics.PeerLabel(peer.String())).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.BroadcastVoteTriggerWG))
	}

	// Wait for all sends to complete
	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("failed to broadcast vote trigger message to any peers")
	}

	broadcastLogger().Info(context.Background(), "Vote trigger broadcast complete",
		ion.String("msg_id", msg.ID),
		ion.Int("success", successCount),
		ion.Int("total", len(peers)))

	return nil
}

// BroadcastBlockToEveryNodeWithExtraData sends a block to all connected peers and attaches extra metadata.
// The extra map will be merged into msg.Data. Keys in extra override existing keys.
func BroadcastBlockToEveryNodeWithExtraData(h host.Host, block *config.ZKBlock, result bool, extra map[string]string, bls []BLS_Signer.BLSresponse) error {
	if BlockPropagationLocalGRO == nil {
		var err error
		BlockPropagationLocalGRO, err = GROHelper.InitializeGRO(GRO.BlockPropagationLocal)
		if err != nil {
			broadcastLogger().Error(context.Background(), "Failed to initialize BlockPropagationLocalGRO", err)
			return err
		}
	}
	broadcastLogger().Info(context.Background(), "Broadcasting block to all nodes (with extra data)",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Uint64("block_number", block.BlockNumber),
		ion.Bool("process_result", result))

	peers := h.Network().Peers()
	if len(peers) == 0 {
		broadcastLogger().Warn(context.Background(), "No connected peers to broadcast block to — skipping broadcast, caller handles local processing")
		return nil
	}

	nonceBytes := make([]byte, 16)
	for i := range nonceBytes {
		nonceBytes[i] = byte(time.Now().UTC().UnixNano() & 0xff)
		time.Sleep(1 * time.Nanosecond)
	}
	nonce := base64.URLEncoding.EncodeToString(nonceBytes)

	now := time.Now().UTC().Unix()
	data := map[string]string{
		"block_hash":   block.BlockHash.Hex(),
		"block_number": fmt.Sprintf("%d", block.BlockNumber),
		"txn_count":    fmt.Sprintf("%d", len(block.Transactions)),
		"proof_hash":   block.ProofHash,
		"status":       block.Status,
		"timestamp":    fmt.Sprintf("%d", block.Timestamp),
	}
	// Merge extras
	for k, v := range extra {
		data[k] = v
	}

	// Attach BLS results (typed) as JSON string under data["bls_results"]
	if len(bls) > 0 {
		if enc, err := json.Marshal(bls); err == nil {
			data["bls_results"] = string(enc)
		}
	}

	msg := config.BlockMessage{
		Sender:    h.ID().String(),
		Timestamp: now,
		Nonce:     nonce,
		Block:     block,
		Type:      "zkblock",
		Hops:      0,
		Data:      data,
	}

	msg.ID = generateBlockMessageID(msg.Sender, nonce, now)
	markMessageProcessed(getBlockDedupID(msg))

	// A rejection notice is discarded by every receiver without applying
	// anything (HandleReceivedBlockMessage, status == "rejected"), so it never
	// needs the proof. Send the header-only copy: same hashes, no StarkProof.
	if !result {
		msg.Block = stripProof(block)
		return sendBlockMessageDirect(h, msg, peers, "rejected-notice")
	}

	// Additively fan the finalized, certified block out over the gossip mesh so it
	// reaches the whole fleet, not only directly-connected peers. Receivers run the
	// same fail-closed admitZKBlock gate. No-op when disabled/unwired; the
	// direct-stream broadcast below is unchanged. The sender already marked this
	// message processed above, so it will not re-ingest its own gossiped copy.
	PublishBlockGossip(msg)

	// Certificate-only finalize (JMDN_FINALIZE_CERT_ONLY / consensus.flags.
	// finalize_cert_only): committee members already hold the full proposal from
	// the vote trigger, so they only need the certificate to apply it. Send them a
	// small zkblock_cert message instead of the 2+ MB block; everyone else gets
	// the full block over gossip. Requires gossip, otherwise non-committee peers
	// would have no path to the block. A receiver on an older binary ignores the
	// unknown type and still applies the gossiped copy, so this is safe to enable
	// on a mixed fleet; it is OFF by default until the fleet runs this code.
	if FinalizeCertOnly && EnableBlockGossip {
		certMsg := msg
		certMsg.Type = blockCertMessageType
		certMsg.Block = stripProof(block)
		targets := finalizeCommitteeTargets(h, block, bls)
		if len(targets) > 0 {
			if err := sendBlockMessageDirect(h, certMsg, targets, "cert-only"); err != nil {
				broadcastLogger().Warn(context.Background(), "cert-only finalize direct send failed; gossip still carries the block",
					ion.Err(err))
			}
		}
		return nil
	}

	// Direct + gossip both run by default (consensus.p2p defaults to 1); dedup and
	// the per-block-hash apply lock make the double delivery safe. Skip the direct
	// per-peer fan-out below ONLY when the operator has opted into gossip-only
	// (consensus.p2p: 0) AND gossip is on — the gossip mesh + FloodPublish then
	// reach the whole fleet. If gossip is OFF, keep direct as the only path so a
	// block is never un-propagated.
	if EnableBlockGossip && !directBlockPropagationEnabled() {
		return nil
	}

	return sendBlockMessageDirect(h, msg, peers, "full")
}

// ProcessBlockLocally processes a block locally after consensus is verified.
// Returns the list of contracts deployed in the block so the sequencer can propagate them.
// blsResults must be non-empty; consensus is verified before any state changes are made.
func ProcessBlockLocally(block *config.ZKBlock, blsResults []BLS_Signer.BLSresponse) error {
	broadcastLogger().Info(context.Background(), "Processing block locally",
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Uint64("block_number", block.BlockNumber),
		ion.Int("bls_results_count", len(blsResults)))

	// D-858 item 2: this is the sequencer's consensus terminal for the height —
	// release the ingress in-flight claim (Block/Server.go) on ANY outcome (commit or
	// failure), so the next height, or a legitimate retry of a failed height, is
	// admitted promptly rather than waiting for the proposalguard TTL. No-op on nodes
	// that never claimed (validators reach state via blockPropagation, not here).
	defer proposalguard.Default.Release(block.BlockNumber)

	// Validate BLS/consensus if results are provided
	// This ensures we only process blocks that have reached consensus
	if len(blsResults) > 0 {
		// Single verifier: route through the one shared certificate verifier
		// — no local quorum math. It fails closed on a missing/failing committee
		// source, de-duplicates by peer_id AND bls_pub, and requires 2f+1 over the
		// authenticated committee size (never a simple majority of whoever
		// responded).
		rc := RoundContextForBlock(block)
		res, err := VerifyCertificateForRound(blsResults, block.BlockHash.Hex(), block.ConsensusHashHex(), block.BlockNumber, rc)
		res, err = relaxForTrustedSequencer("process_block_locally", res, err, blsResults, block.BlockHash.Hex(), block.ConsensusHashHex(), block.BlockNumber)
		if err != nil {
			broadcastLogger().Error(context.Background(), "refusing consensus participation (fail closed): committee eligibility source invalid", err,
				ion.String("block_hash", block.BlockHash.Hex()))
			return fmt.Errorf("committee eligibility source invalid (fail closed): %w", err)
		}
		if !res.Reached {
			broadcastLogger().Error(context.Background(), "BFT 2f+1 not reached - skipping block processing (consensus not reached)",
				errors.New("consensus not reached"),
				ion.String("block_hash", block.BlockHash.Hex()),
				ion.Int("valid_yes", res.YesVotes),
				ion.Int("needed", res.Threshold),
				ion.Int("committee_size", res.CommitteeSize))
			// Explorer lifecycle: FAILED — quorum not reached for this block's txs.
			lifecycle.MarkFailed(block.BlockNumber, "consensus not reached (2f+1)")
			return fmt.Errorf("consensus not reached for block %s: %d eligible +1 votes, need %d (2f+1 over committee size %d)",
				block.BlockHash.Hex(), res.YesVotes, res.Threshold, res.CommitteeSize)
		}

		broadcastLogger().Info(context.Background(), "BFT 2f+1 verified - consensus reached",
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Int("valid_yes", res.YesVotes),
			ion.Int("needed", res.Threshold),
			ion.Int("committee_size", res.CommitteeSize))

		// Explorer lifecycle: EXECUTING (validating) — the certificate cleared 2f+1.
		// "5/7 validated" = YesVotes/CommitteeSize; Threshold is the needed 2f+1.
		lifecycle.MarkExecuting(block.BlockNumber, lifecycle.Progress{
			SubStage:      "validating",
			YesVotes:      res.YesVotes,
			CommitteeSize: res.CommitteeSize,
			Threshold:     res.Threshold,
		})

	} else {
		// BLS results are required to ensure consensus was reached
		// If no BLS results are provided, we cannot verify consensus and should not process
		broadcastLogger().Error(context.Background(), "No BLS results provided - cannot verify consensus, refusing to process block",
			errors.New("empty BLS results"),
			ion.String("block_hash", block.BlockHash.Hex()))
		return fmt.Errorf("cannot process block %s without BLS results to verify consensus", block.BlockHash.Hex())
	}

	// Persist the committee certificate that was just verified above (2f+1
	// fail-closed) so it survives past the in-memory blsResults and is
	// re-verifiable on sync (P-cert / ThebeSync). Advisory field; does not affect
	// BlockHash/ConsensusHash/StateFingerprint. Set BEFORE apply+store below so the
	// stored block carries it. Best-effort marshal — a marshal error must not block
	// a validated block from being stored.
	if len(blsResults) > 0 {
		if certJSON, mErr := json.Marshal(blsResults); mErr == nil {
			block.CommitteeCertificate = string(certJSON)
		} else {
			broadcastLogger().Warn(context.Background(), "committee certificate marshal failed (block stored without persisted cert)",
				ion.String("block_hash", block.BlockHash.Hex()),
				ion.String("error", mErr.Error()))
		}
	}

	// Apply transactions and store the block in ONE fail-closed step (D-858).
	// ProcessBlockTransactionsAndStore stores the block AFTER the state-fingerprint
	// check and BEFORE the block-processed marker; if StoreZKBlock fails, it rolls
	// back the full applied prefix (balances, tx_nonce, tx_count_sent, fee/coinbase/
	// zkvm credits, per-tx markers) and drops the outbox projection — so a block that
	// is not durably stored leaves NO state change. This is the block-858 fix: the
	// old code applied a second duplicate-height candidate's txs and then returned on
	// a StoreZKBlock uq_txn_block_index failure WITHOUT rolling back, advancing the
	// sequencer's account state past the fleet and forcing a validator re-seed.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := BlockProcessing.ProcessBlockTransactionsAndStore(ctx, block, nil, func() error {
		return DB_OPs.StoreZKBlock(nil, block)
	}); err != nil {
		broadcastLogger().Error(context.Background(), "Block apply+store failed - block not applied (rolled back, withheld from peers)", err,
			ion.String("block_hash", block.BlockHash.Hex()),
			ion.Uint64("block_number", block.BlockNumber))
		// Explorer lifecycle: FAILED — either tx processing or the durable store failed.
		lifecycle.MarkFailed(block.BlockNumber, "block apply or store failed")
		return fmt.Errorf("failed to process/store block: %w", err)
	}

	// Explorer lifecycle: SUCCESS — block stored + applied. The status endpoint's
	// chain-first check would report this anyway; the explicit mark makes the SSE
	// transition immediate rather than waiting for the next poll.
	lifecycle.MarkCommitted(block.BlockNumber)

	// Full block stored + processed → advance the tip marker.
	// Monotonic: a replayed/out-of-order block can never regress it.
	// StoreZKBlock no longer writes the marker itself (skeleton safety).
	// W1: the sequencer's own committed anchor block fixes the next selection
	// period's pool on this node too (receivers record it in admitZKBlock).
	if aerr := RecordCommitteeAnchor(block); aerr != nil {
		broadcastLogger().Error(context.Background(), "committee anchor: could not record the pool defined by this anchor block", aerr,
			ion.Uint64("block_number", block.BlockNumber))
	}
	if _, _, err := DB_OPs.UpdateLatestBlockMonotonic(block.BlockNumber); err != nil {
		broadcastLogger().Warn(context.Background(), "latest_block monotonic update failed (non-fatal: ReconcileBlockNumber heals forward)",
			ion.String("error", err.Error()),
			ion.Uint64("block_number", block.BlockNumber))
	}

	// M0.1 (Architecture §7.1) — fold this now-committed height into the
	// live slot counter, using its OWN final Period (how many certified
	// timeouts it burned before committing). Producer-side commit hook; see
	// slot_store.go's header comment for the receiver-side twin and the
	// documented live-only/no-persistence limitation.
	DefaultSlotStore.AdvanceOnCommit(block.BlockNumber, block.Period)
	NoteHeightCommitted(block.BlockNumber)

	// M4 §C/§D + B1 + Stage-F receive side, in one place so the live path and
	// the sync path (thebesync) cannot diverge. See entropy_block_effects.go
	// for the order and why each step must come where it does.
	ApplyBlockEntropyEffects(block)

	// Committee-signed chain-head checkpoint (Option A anchor). No-op unless
	// checkpoint.enabled AND this is the sequencer AND the cadence fires; a
	// best-effort side observer that never affects the commit above.
	maybeSignChainHeadCheckpoint(block)

	// Address→tx lookups are served by the ThebeDB SQL projection, which the
	// commit above already wrote (gateway 2PC) — no separate index to update.

	broadcastLogger().Info(context.Background(), "Block processed and stored successfully",
		ion.Uint64("block_number", block.BlockNumber),
		ion.String("block_hash", block.BlockHash.Hex()),
		ion.Int("tx_count", len(block.Transactions)))

	return nil
}

// blockCertMessageType is the certificate-only finalize envelope: Block carries
// the header fields (hashes, number, status) but no StarkProof; Data carries the
// committee certificate. The receiver substitutes the proposal it cached at
// vote-trigger time (HandleReceivedBlockMessage).
const blockCertMessageType = "zkblock_cert"

// stripProof returns a shallow copy of block with the StarkProof removed. Hashes
// (BlockHash, ConsensusHash) are stored fields and are unaffected.
func stripProof(block *config.ZKBlock) *config.ZKBlock {
	if block == nil {
		return nil
	}
	cp := *block
	cp.StarkProof = nil
	return &cp
}

// finalizeCommitteeTargets is the set of connected peers known to hold the
// proposal: the voters in the certificate plus the round's committee if the
// sequencer cached the consensus message. Anyone else gets the block via gossip.
func finalizeCommitteeTargets(h host.Host, block *config.ZKBlock, bls []BLS_Signer.BLSresponse) []peer.ID {
	want := make(map[peer.ID]struct{}, len(bls))
	for _, r := range bls {
		if pid, err := peer.Decode(r.PeerID); err == nil {
			want[pid] = struct{}{}
		}
	}
	if cm := PubSubMessages.LookupConsensusMessageByBlockHash(block.BlockHash.Hex()); cm != nil {
		for _, b := range cm.GetBuddies() {
			if b.PeerID != "" {
				want[b.PeerID] = struct{}{}
			}
		}
	}
	self := h.ID()
	targets := make([]peer.ID, 0, len(want))
	for _, p := range h.Network().Peers() {
		if p == self {
			continue
		}
		if _, ok := want[p]; ok {
			targets = append(targets, p)
		}
	}
	return targets
}

// sendBlockMessageDirect writes msg to each peer over BlockPropagationProtocol,
// one bounded goroutine per peer, and logs the fan-out size under scope.
func sendBlockMessageDirect(h host.Host, msg config.BlockMessage, peers []peer.ID, scope string) error {
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal block message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	wg, err := BlockPropagationLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.BlockPropagationForwardWG)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to create waitgroup for block propagation forwarding", err)
		return fmt.Errorf("failed to create waitgroup for block propagation forwarding: %w", err)
	}

	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		peer := peerID
		BlockPropagationLocalGRO.Go(GRO.BlockPropagationForwardThread, func(ctx context.Context) error {
			ctxWithTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			stream, err := h.NewStream(ctxWithTimeout, peer, config.BlockPropagationProtocol)
			if err != nil {
				broadcastLogger().Debug(context.Background(), "Failed to open stream",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}
			defer stream.Close()
			_ = stream.SetWriteDeadline(time.Now().Add(broadcastWriteTimeout))
			if _, err := stream.Write(msgBytes); err != nil {
				_ = stream.Reset()
				broadcastLogger().Debug(context.Background(), "Failed to write message",
					ion.Err(err),
					ion.String("peer", peer.String()))
				return err
			}
			successMutex.Lock()
			successCount++
			successMutex.Unlock()
			metrics.MessagesSentCounter.WithLabelValues(metrics.ProtocolLabel(msg.Type), metrics.PeerLabel(peer.String())).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.BlockPropagationForwardWG))
	}

	wg.Wait()

	broadcastLogger().Info(context.Background(), "Block broadcast complete (with extra data)",
		ion.String("block_hash", msg.Block.BlockHash.Hex()),
		ion.String("scope", scope),
		ion.Int("success", successCount),
		ion.Int("total", len(peers)))

	return nil
}
