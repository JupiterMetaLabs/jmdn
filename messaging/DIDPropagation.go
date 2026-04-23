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
			// Create accounts database client if none provided
			ctx := context.Background()
			client, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
			if err != nil {
				initErr = fmt.Errorf("failed to create accounts database client: %w", err)
				return
			}

			accountsMutex.Lock()
			accountsClient = client
			accountsMutex.Unlock()
			broadcastLogger().Info(context.Background(), "DID propagation system initialized with new database client")
		}
	})

	return initErr
}

// generateAccountMessageID creates a unique ID for a Account message
func generateAccountMessageID(sender string, Account common.Address) string {
	hasher := sha256.New()
	hasher.Write([]byte(fmt.Sprintf("%s-%s", sender, Account.Hex())))
	hash := base64.URLEncoding.EncodeToString(hasher.Sum(nil))
	return hash[:16] // Return first 16 chars for brevity
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

		// Store Account document
		err := DB_OPs.CreateAccount(client, msg.Account.DIDAddress, msg.Account.Address, nil)
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

	// Get the remote peer
	remotePeer := stream.Conn().RemotePeer().String()

	// Record metrics
	metrics.MessagesReceivedCounter.WithLabelValues("did", remotePeer).Inc()

	// Read the incoming message
	reader := bufio.NewReader(stream)
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

	// Generate a unique ID based on sender, DID and timestamp
	msg.ID = generateAccountMessageID(msg.Sender, doc.Address)

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

	// Get all connected peers
	peers := h.Network().Peers()
	if len(peers) == 0 {
		broadcastLogger().Warn(context.Background(), "No connected peers to propagate DID to",
			ion.Err(errors.New("no peers")),
			ion.String("did", doc.DIDAddress),
			ion.String("type", msgType))
		return nil // Not an error, just no one to tell
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

	broadcastLogger().Info(context.Background(), "DID propagation complete",
		ion.String("msg_id", msg.ID),
		ion.String("did", doc.DIDAddress),
		ion.String("public_key", doc.Address.Hex()),
		ion.String("balance", doc.Balance),
		ion.String("type", msgType),
		ion.Int("success", successCount),
		ion.Int("total", len(peers)))

	return nil
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
