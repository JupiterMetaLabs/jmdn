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

	"gossipnode/config/GRO"
	GROHelper "gossipnode/messaging/common"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/bits-and-blooms/bloom/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/metrics"
)

// DIDMessage represents a message for DID propagation
type DIDMessage struct {
	ID        string          `json:"id"`
	Sender    string          `json:"sender"`
	Timestamp int64           `json:"timestamp"`
	Type      string          `json:"type"` // "did_created", "did_updated", etc.
	Hops      int             `json:"hops"`
	Account   *DB_OPs.Account `json:"account,omitempty"`
}

// Store for DID message tracking
var (
	accountFilter  *bloom.BloomFilter
	accountsClient *config.PooledConnection
	accountsMutex  sync.RWMutex
	accountOnce    sync.Once
)

// InitDIDPropagation initializes the DID propagation system
func InitDIDPropagation(existingClient *config.PooledConnection) error {
	var initErr error

	accountOnce.Do(func() {
		// Initialize the bloom filter for DID messages
		accountFilter = bloom.NewWithEstimates(100000, 0.01)

		if existingClient != nil {
			// Use the provided client instead of creating a new one
			accountsMutex.Lock()
			accountsClient = existingClient
			accountsMutex.Unlock()
			broadcastLogger().Info(context.Background(), "DID propagation system initialized with existing database client")
		} else {
			// No existing client provided; DB_OPs calls use globalThebeHandle via getHandle(nil).
			broadcastLogger().Info(context.Background(), "DID propagation system initialized with new database client")
		}
	})

	return initErr
}

// deriveMessageID builds the bloom-filter dedup key from the account address
// alone. The key is therefore identical on every node and at every hop, so a
// given account is deduped consistently network-wide, and it cannot be varied
// by a peer to defeat dedup. An account address maps to a single creation
// event, so keying on the address is sufficient for this channel.
func deriveMessageID(addr common.Address) string {
	sum := sha256.Sum256([]byte(addr.Hex()))
	return base64.URLEncoding.EncodeToString(sum[:])[:24]
}

// isAccountMessageProcessed checks if this message has already been processed
func isAccountMessageProcessed(messageID string) bool {
	// Initialize filter if not already done
	if accountFilter == nil {
		accountOnce.Do(func() {
			accountFilter = bloom.NewWithEstimates(100000, 0.01)
		})
	}
	return accountFilter.Test([]byte(messageID))
}

// markAccountMessageProcessed marks a message as processed
func markAccountMessageProcessed(messageID string) {
	// Initialize filter if not already done
	if accountFilter == nil {
		accountOnce.Do(func() {
			accountFilter = bloom.NewWithEstimates(100000, 0.01)
		})
	}
	accountFilter.Add([]byte(messageID))
}

// storeAccountInDB stores the Account document in the accounts database
func storeAccountInDB(msg DIDMessage) {
	if DIDLocalGRO == nil {
		var err error
		DIDLocalGRO, err = GROHelper.InitializeGRO(GRO.DIDPropagationLocal)
		if err != nil {
			broadcastLogger().Error(context.Background(), "Failed to initialize LocalGRO", err)
			return
		}
	}
	// Check if Account data is present
	if msg.Account == nil {
		broadcastLogger().Warn(context.Background(), "Received DID message with no account data, skipping storage",
			ion.Err(errors.New("no account data")),
			ion.String("msg_id", msg.ID),
			ion.String("sender", msg.Sender))
		return
	}

	// Store in accounts database in a separate goroutine to prevent blocking
	DIDLocalGRO.Go(GRO.DIDStoreThread, func(ctx context.Context) error {
		accountsMutex.RLock()
		if accountsClient == nil {
			broadcastLogger().Error(ctx, "Accounts client not initialized", errors.New("accounts client not initialized"))
			accountsMutex.RUnlock()
			return fmt.Errorf("accounts client not initialized")
		}
		client := accountsClient
		accountsMutex.RUnlock()

		// Create Account document
		// accountDoc := &DB_OPs.Account{
		// 	DIDAddress:  msg.Account.DIDAddress,
		// 	Address:     msg.Account.Address,
		// 	Balance:     msg.Account.Balance,
		// 	Nonce:       msg.Account.Nonce,
		// 	CreatedAt:   msg.Timestamp,
		// 	Metadata:    msg.Account.Metadata,
		// 	AccountType: msg.Account.AccountType,
		// 	UpdatedAt:   time.Now().UTC().Unix(),
		// }

		// Store Account document preserving the sender's ART Nonce
		err := DB_OPs.StorePropagatedAccount(client, msg.Account)
		if err != nil {
			broadcastLogger().Error(ctx, "Failed to store Account in database", err, ion.String("Account", msg.Account.DIDAddress))
			return err
		}

		broadcastLogger().Info(ctx, "Successfully stored DID in database", ion.String("Account", msg.Account.DIDAddress))

		// Also update the DID set (CRDT)
		// err = updateDIDSet(client, msg.DID)
		// if err != nil {
		//     log.Error().Err(err).Str("did", msg.DID).Msg("Failed to update DID set")
		// }
		return nil
	})
}

/* UNUSED
// updateDIDSet adds a DID to the grow-only set in accounts database
func updateDIDSet(client *config.PooledConnection, did string) error {
	const setKey = "crdt:did_set"

	// Try to get the current set
	var didSet map[string]bool
	err := DB_OPs.ReadJSON(setKey, &didSet)

	// If not found or error, start with empty set
	if err != nil {
		didSet = make(map[string]bool)
	}

	// Add the new DID (idempotent operation)
	didSet[did] = true

	// Store the updated set
	return DB_OPs.Create(client, setKey, didSet)
}
*/

// maxDIDFrameBytes caps the JSON frame size for a single DID message. Account
// documents are well under this bound; the cap keeps reads bounded and memory
// use predictable.
const maxDIDFrameBytes = 64 * 1024 // 64 KB

// HandleDIDStream processes incoming DID propagation messages
func HandleDIDStream(stream network.Stream) {
	if DIDLocalGRO == nil {
		var err error
		DIDLocalGRO, err = GROHelper.InitializeGRO(GRO.DIDPropagationLocal)
		if err != nil {
			broadcastLogger().Error(context.Background(), "Failed to initialize LocalGRO", err)
			return
		}
	}
	defer stream.Close()

	// Transport peer — used to derive a content ID and override msg.Sender.
	remotePeer := stream.Conn().RemotePeer().String()

	// Record metrics
	metrics.MessagesReceivedCounter.WithLabelValues("did", remotePeer).Inc()

	// Read the incoming message with a bounded frame size.
	reader := bufio.NewReader(io.LimitReader(stream, maxDIDFrameBytes))
	messageBytes, err := reader.ReadBytes('\n')
	if err != nil {
		if err != io.EOF {
			broadcastLogger().Error(context.Background(), "Error reading DID message", err, ion.String("peer", remotePeer))
		}
		return
	}

	// Parse the message
	var msg DIDMessage
	if err := json.Unmarshal(messageBytes, &msg); err != nil {
		broadcastLogger().Error(context.Background(), "Failed to unmarshal DID message", err)
		return
	}

	// Drop messages with no usable account data early.
	if msg.Account == nil || msg.Account.Address == (common.Address{}) {
		broadcastLogger().Warn(context.Background(), "DID message missing valid account, dropping", ion.String("peer", remotePeer))
		return
	}

	// Use the transport peer identity as the authoritative origin for dedup,
	// routing, and logging rather than the field carried in the message.
	msg.Sender = remotePeer

	// Derive the dedup key from the account address so re-announcements of the
	// same account map to a stable, network-wide key.
	msg.ID = deriveMessageID(msg.Account.Address)

	// Normalize the volatile ledger fields synchronously here, before both
	// storage and re-forwarding. StorePropagatedAccount applies the same policy,
	// but it runs in a goroutine (storeAccountInDB) and mutates the shared
	// msg.Account pointer concurrently with the forward below; normalizing once
	// on this object keeps the stored and forwarded copies consistent and
	// deterministic.
	if DB_OPs.NormalizePropagatedAccountState(msg.Account) {
		broadcastLogger().Debug(context.Background(), "Normalized propagated account ledger fields at ingress",
			ion.String("msg_id", msg.ID),
			ion.String("peer", remotePeer),
			ion.String("account", msg.Account.Address.Hex()))
	}

	// Check if we've already processed this message
	if isAccountMessageProcessed(msg.ID) {
		broadcastLogger().Debug(context.Background(), "Duplicate Account message received", ion.String("message_id", msg.ID))
		return
	}

	// Mark message as processed
	markAccountMessageProcessed(msg.ID)

	// Process the message - update our Account database
	storeAccountInDB(msg)

	// Log receipt (with nil check)
	// if msg.Account != nil {
	// 	fmt.Printf("\n[DID from %s] DID: %s, Address: %s\n>>> ", msg.Sender, msg.Account.DIDAddress, msg.Account.Address)
	// } else {
	// 	fmt.Printf("\n[DID from %s] DID message received (no account data)\n>>> ", msg.Sender)
	// }

	// Only rebroadcast if we haven't reached max hops and have account data
	if msg.Hops < config.MaxAccountHops && msg.Account != nil {
		// Forward to our peers
		msg.Hops++
		localPeer := stream.Conn().LocalPeer().String()
		broadcastLogger().Info(context.Background(), "Propagating Account message",
			ion.String("msg_id", msg.ID),
			ion.String("type", msg.Type),
			ion.String("origin", msg.Sender),
			ion.String("via", localPeer),
			ion.String("account", msg.Account.Address.Hex()),
			ion.Int("hops", msg.Hops))

		// Forward the message to other peers
		if hostInstance := getHostInstance(); hostInstance != nil {
			DIDLocalGRO.Go(GRO.DIDPropagationStreamThread, func(ctx context.Context) error {
				forwardDID(hostInstance, msg)
				return nil
			})
		} else {
			broadcastLogger().Error(context.Background(), "Cannot access host instance for forwarding DID message", errors.New("host instance not available"))
		}
	} else if msg.Account != nil {
		broadcastLogger().Info(context.Background(), "Max hops reached, not propagating Account message",
			ion.String("msg_id", msg.ID),
			ion.String("type", msg.Type),
			ion.String("account", msg.Account.Address.Hex()),
			ion.Int("hops", msg.Hops))
	} else {
		if msg.Account == nil {
			broadcastLogger().Info(context.Background(), "Account data is nil, not propagating Account message",
				ion.String("msg_id", msg.ID),
				ion.String("type", msg.Type),
				ion.Int("hops", msg.Hops))
		}
	}
}

// forwardDID sends the DID message to all connected peers
func forwardDID(h host.Host, msg DIDMessage) {
	// Get all connected peers
	peers := h.Network().Peers()

	// Convert message to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to marshal DID message", err)
		return
	}
	msgBytes = append(msgBytes, '\n')

	// Track how many peers we successfully broadcasted to
	var successCount int
	var successMutex sync.Mutex

	// Create waitgroup for tracking goroutines
	wg, err := DIDLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.DIDForwardThread)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to create waitgroup for DID forwarding", err)
		return
	}

	// Send to each peer (except original sender) concurrently
	for _, peerID := range peers {
		// Don't send back to the original sender
		if peerID.String() == msg.Sender {
			continue
		}

		// Capture peerID in closure to avoid race condition
		peerIDForGoroutine := peerID
		if err := DIDLocalGRO.Go(GRO.DIDForwardThread, func(ctx context.Context) error {
			stream, err := h.NewStream(ctx, peerIDForGoroutine, config.DIDPropagationProtocol)
			if err != nil {
				broadcastLogger().Error(ctx, "Failed to open DID stream", err, ion.String("peer", peerIDForGoroutine.String()))
				return err
			}
			defer stream.Close()

			// Write the message
			_, err = stream.Write(msgBytes)
			if err != nil {
				broadcastLogger().Error(ctx, "Failed to write DID message", err, ion.String("peer", peerIDForGoroutine.String()))
				return err
			}

			// Increment success count and record metrics
			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			// Record metrics
			metrics.MessagesSentCounter.WithLabelValues("did", peerIDForGoroutine.String()).Inc()

			return nil
		}, local.AddToWaitGroup(GRO.DIDForwardWG)); err != nil {
			broadcastLogger().Error(context.Background(), "Failed to start goroutine for DID forwarding", err, ion.String("peer", peerIDForGoroutine.String()))
		}
	}

	// Wait for all sends to complete
	wg.Wait()

	broadcastLogger().Info(context.Background(), "Account message propagated to peers",
		ion.String("msg_id", msg.ID),
		ion.String("type", msg.Type),
		ion.String("address", msg.Account.Address.Hex()),
		ion.Int("hops", msg.Hops),
		ion.Int("peers", successCount))
}

// PropagateDID creates and propagates a DID message to the network
func PropagateDID(h host.Host, doc *DB_OPs.Account) error {
	if DIDLocalGRO == nil {
		var err error
		DIDLocalGRO, err = GROHelper.InitializeGRO(GRO.DIDPropagationLocal)
		if err != nil {
			broadcastLogger().Error(context.Background(), "Failed to initialize LocalGRO", err)
			return fmt.Errorf("failed to initialize LocalGRO: %w", err)
		}
	}
	if doc == nil {
		return fmt.Errorf("DID document cannot be nil")
	}

	// Determine message type based on document timestamps
	msgType := "did_created"
	if doc.UpdatedAt > doc.CreatedAt {
		// If updated time is greater than created time, this is an update
		msgType = "did_updated"
	}

	// Create a DID message
	now := time.Now().UTC().Unix()
	msg := DIDMessage{
		Sender:    h.ID().String(),
		Timestamp: now,
		Type:      msgType,
		Account:   doc,
		Hops:      0,
	}

	// Derive the dedup key from the account address (same scheme as HandleDIDStream).
	msg.ID = deriveMessageID(doc.Address)

	// First, add/update the DID in our own database
	storeAccountInDB(msg)

	// Mark this message as processed by us
	markAccountMessageProcessed(msg.ID)

	// Convert to JSON
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal DID message: %w", err)
	}
	msgBytes = append(msgBytes, '\n')

	// Get all connected peers.
	//
	// NOTE ON TRANSPORT: this is a fan-out of per-peer UNICAST libp2p streams,
	// not pubsub. Only peers connected at this instant are reached directly;
	// everyone else depends on receive-side hop-forwarding (MaxHops).
	peers := h.Network().Peers()
	if len(peers) == 0 {
		broadcastLogger().Error(context.Background(), "No connected peers to propagate DID to",
			errors.New("no connected peers"),
			ion.String("did", doc.DIDAddress),
			ion.String("type", msgType))
		// This used to `return nil // Not an error, just no one to tell`.
		//
		// It IS an error. The account now exists in this node's database and
		// nowhere else, and because the dedup message ID is derived from the
		// address alone (see deriveMessageID), a later re-propagation of the
		// same address is dropped as a duplicate by every peer — so this is
		// unrecoverable, not merely delayed. Reporting success here let the
		// sequencer log "Auto-created and propagated DID" and let the
		// orchestrator propose a block whose receiver no voter could see,
		// which every committee member then rejected.
		return fmt.Errorf("cannot propagate DID %s: no connected peers", doc.DIDAddress)
	}

	broadcastLogger().Info(context.Background(), "Starting DID propagation to peers",
		ion.String("msg_id", msg.ID),
		ion.String("did", doc.DIDAddress),
		ion.String("public_key", doc.Address.Hex()),
		ion.String("balance", doc.Balance),
		ion.String("type", msgType),
		ion.Int("peers", len(peers)))

	// Send message to all peers
	// Create waitgroup for tracking goroutines
	wg, err := DIDLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.DIDForwardThread)
	if err != nil {
		broadcastLogger().Error(context.Background(), "Failed to create waitgroup for DID forwarding", err)
		return fmt.Errorf("failed to create waitgroup for DID forwarding: %w", err)
	}
	var successCount int
	var successMutex sync.Mutex

	for _, peerID := range peers {
		// make closure for peerID
		peerIDForGoroutine := peerID
		if err := DIDLocalGRO.Go(GRO.DIDPropagationThread, func(ctx context.Context) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			// Open stream to peer
			stream, err := h.NewStream(ctx, peerIDForGoroutine, config.DIDPropagationProtocol)
			if err != nil {
				broadcastLogger().Error(ctx, "Failed to open stream for DID", err, ion.String("peer", peerIDForGoroutine.String()))
				return err
			}
			defer stream.Close()

			// Send the message
			_, err = stream.Write(msgBytes)
			if err != nil {
				broadcastLogger().Error(ctx, "Failed to send DID message", err, ion.String("peer", peerIDForGoroutine.String()))
				return err
			}

			// Record success
			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			// Record metrics
			metrics.MessagesSentCounter.WithLabelValues("did", peerIDForGoroutine.String()).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.DIDForwardWG)); err != nil {
			broadcastLogger().Error(context.Background(), "Failed to start goroutine for DID propagation", err, ion.String("peer", peerID.String()))
		}
	}

	// Wait for all sends to complete
	wg.Wait()

	// successCount was previously computed and then DISCARDED — this function
	// returned nil even when every single send failed. Callers
	// (CreateAccountandPropagateDID → eth_getBalance auto-create) treated that
	// as proof the fleet had the account. Report the truth instead.
	successMutex.Lock()
	delivered := successCount
	successMutex.Unlock()

	committeeReached, committeeSize, committeeErr := committeeDeliveryStatus(peers, delivered)

	if delivered == 0 {
		broadcastLogger().Error(context.Background(), "DID propagation complete — delivered to ZERO peers",
			errors.New("did propagation delivered to zero peers"),
			ion.String("msg_id", msg.ID),
			ion.String("did", doc.DIDAddress),
			ion.String("public_key", doc.Address.Hex()),
			ion.String("balance", doc.Balance),
			ion.String("type", msgType),
			ion.Int("success", delivered),
			ion.Int("total", len(peers)),
			ion.Int("committee_reached", committeeReached),
			ion.Int("committee_size", committeeSize))
	} else {
		broadcastLogger().Info(context.Background(), "DID propagation complete",
			ion.String("msg_id", msg.ID),
			ion.String("did", doc.DIDAddress),
			ion.String("public_key", doc.Address.Hex()),
			ion.String("balance", doc.Balance),
			ion.String("type", msgType),
			ion.Int("success", delivered),
			ion.Int("total", len(peers)),
			ion.Int("committee_reached", committeeReached),
			ion.Int("committee_size", committeeSize))
	}

	if delivered == 0 {
		return fmt.Errorf("failed to propagate DID %s to any of %d connected peers",
			doc.DIDAddress, len(peers))
	}

	// When this node knows the consensus committee (the sequencer does), a
	// delivery that cannot possibly satisfy a vote is worth failing on now
	// rather than discovering it as a rejected block a minute later. Voters
	// reject any transaction whose receiver is absent from their account cache
	// (Security/security_cache.go), so short of a Byzantine quorum of committee
	// members holding the account, the block is already lost.
	//
	// Fail-open when the committee is unknown (committeeErr != nil): non-sequencer
	// nodes have no eligibility source wired and must keep propagating normally.
	if committeeErr == nil && committeeSize > 0 {
		if need := ByzantineQuorum(committeeSize); committeeReached < need {
			return fmt.Errorf("DID %s reached only %d of %d committee members directly (need %d); "+
				"a block using this account would be rejected",
				doc.DIDAddress, committeeReached, committeeSize, need)
		}
	}

	return nil
}

// committeeDeliveryStatus reports how much of the consensus committee this
// propagation could have reached directly.
//
// It is deliberately CONSERVATIVE and approximate: libp2p gives no per-peer
// delivery receipt here, so it attributes the observed success count to the
// committee members that were among the target peers, capped by both. A
// receive-side hop-forward may still deliver to committee members that were not
// directly connected, so a low count is a warning about the direct path, not
// proof of non-delivery.
//
// Returns (reached, committeeSize, err). A non-nil err means the committee is
// unknown on this node — callers must fail OPEN in that case.
func committeeDeliveryStatus(targets []peer.ID, delivered int) (reached, size int, err error) {
	members, err := eligibleMembers()
	if err != nil {
		return 0, 0, err
	}
	size = len(members)
	if size == 0 {
		return 0, 0, nil
	}
	inTargets := 0
	for _, p := range targets {
		if _, ok := members[p.String()]; ok {
			inTargets++
		}
	}
	reached = inTargets
	if delivered < reached {
		// Fewer sends succeeded than there were committee targets; we cannot
		// tell which failed, so assume the worst.
		reached = delivered
	}
	return reached, size, nil
}

// ListAllDIDs retrieves all known DIDs from the database
func ListAllDIDs(limit int) ([]*DB_OPs.Account, error) {
	accountsMutex.RLock()
	client := accountsClient
	accountsMutex.RUnlock()

	if client == nil {
		return nil, fmt.Errorf("accounts client not initialized")
	}

	return DB_OPs.ListAllAccounts(client, limit)
}
