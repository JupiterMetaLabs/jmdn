package messaging

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"gossipnode/config/GRO"
	GROHelper "gossipnode/messaging/common"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/bits-and-blooms/bloom/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/rs/zerolog/log"

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
	fmt.Println("Initializing DID propagation system...")
	var initErr error

	accountOnce.Do(func() {
		// Initialize the bloom filter for DID messages
		accountFilter = bloom.NewWithEstimates(100000, 0.01)

		if existingClient != nil {
			// Use the provided client instead of creating a new one
			accountsMutex.Lock()
			accountsClient = existingClient
			accountsMutex.Unlock()
			log.Info().Msg("DID propagation system initialized with existing database client")
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
			log.Info().Msg("DID propagation system initialized with new database client")
		}
	})

	return initErr
}

// generateAccountMessageID creates a unique ID for a Account message
func generateAccountMessageID(sender string, Account common.Address) string {
	hasher := sha256.New()
	fmt.Fprintf(hasher, "%s-%s", sender, Account.Hex())
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
			log.Error().Err(err).Msg("Failed to initialize LocalGRO")
			return
		}
	}
	// Check if Account data is present
	if msg.Account == nil {
		log.Warn().
			Str("msg_id", msg.ID).
			Str("sender", msg.Sender).
			Msg("Received DID message with no account data, skipping storage")
		return
	}

	// Store in accounts database in a separate goroutine to prevent blocking
	DIDLocalGRO.Go(GRO.DIDStoreThread, func(ctx context.Context) error {
		accountsMutex.RLock()
		if accountsClient == nil {
			log.Error().Msg("Accounts client not initialized")
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
			log.Error().Err(err).Str("Account", msg.Account.DIDAddress).Msg("Failed to store Account in database")
			return err
		}

		log.Info().Str("Account", msg.Account.DIDAddress).Msg("Successfully stored DID in database")

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
			log.Error().Err(err).Msg("Failed to initialize LocalGRO")
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
			log.Error().Err(err).Str("peer", remotePeer).
				Msg("Error reading DID message")
		}
		return
	}

	// Parse the message
	var msg DIDMessage
	if err := json.Unmarshal(messageBytes, &msg); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal DID message")
		return
	}

	// Check if we've already processed this message
	if isAccountMessageProcessed(msg.ID) {
		log.Debug().Str("message_id", msg.ID).Msg("Duplicate Account message received")
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
		log.Info().
			Str("msg_id", msg.ID).
			Str("type", msg.Type).
			Str("origin", msg.Sender).
			Str("via", localPeer).
			Str("account", msg.Account.Address.Hex()).
			Int("hops", msg.Hops).
			Msg("Propagating Account message")

		// Forward the message to other peers
		if hostInstance := getHostInstance(); hostInstance != nil {
			DIDLocalGRO.Go(GRO.DIDPropagationStreamThread, func(ctx context.Context) error {
				forwardDID(hostInstance, msg)
				return nil
			})
		} else {
			log.Error().Msg("Cannot access host instance for forwarding DID message")
		}
	} else if msg.Account != nil {
		log.Info().
			Str("msg_id", msg.ID).
			Str("type", msg.Type).
			Str("account", msg.Account.Address.Hex()).
			Int("hops", msg.Hops).
			Msg("Max hops reached, not propagating Account message")
	} else {
		if msg.Account == nil {
			log.Info().
				Str("msg_id", msg.ID).
				Str("type", msg.Type).
				Int("hops", msg.Hops).
				Msg("Account data is nil, not propagating Account message")
			fmt.Printf("MessageID:%s, Type:%s, Hops:%d : Account data is nil, not propagating Account message\n", msg.ID, msg.Type, msg.Hops)
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
		log.Error().Err(err).Msg("Failed to marshal DID message")
		return
	}
	msgBytes = append(msgBytes, '\n')

	// Track how many peers we successfully broadcasted to
	var successCount int
	var successMutex sync.Mutex

	// Create waitgroup for tracking goroutines
	wg, err := DIDLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.DIDForwardThread)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create waitgroup for DID forwarding")
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
				log.Error().Err(err).Str("peer", peerIDForGoroutine.String()).Msg("Failed to open DID stream")
				return err
			}
			defer stream.Close()

			// Write the message
			_, err = stream.Write(msgBytes)
			if err != nil {
				log.Error().Err(err).Str("peer", peerIDForGoroutine.String()).Msg("Failed to write DID message")
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
			log.Error().Err(err).Str("peer", peerIDForGoroutine.String()).Msg("Failed to start goroutine for DID forwarding")
		}
	}

	// Wait for all sends to complete
	wg.Wait()

	log.Info().
		Str("msg_id", msg.ID).
		Str("type", msg.Type).
		Str("address", msg.Account.Address.Hex()).
		Int("hops", msg.Hops).
		Int("peers", successCount).
		Msg("Account message propagated to peers")
}

// PropagateDID creates and propagates a DID message to the network
func PropagateDID(h host.Host, doc *DB_OPs.Account) error {
	if DIDLocalGRO == nil {
		var err error
		DIDLocalGRO, err = GROHelper.InitializeGRO(GRO.DIDPropagationLocal)
		if err != nil {
			log.Error().Err(err).Msg("Failed to initialize LocalGRO")
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
		log.Warn().
			Str("did", doc.DIDAddress).
			Str("type", msgType).
			Msg("No connected peers to propagate DID to")
		return nil // Not an error, just no one to tell
	}

	log.Info().
		Str("msg_id", msg.ID).
		Str("did", doc.DIDAddress).
		Str("public_key", doc.Address.Hex()).
		Str("balance", doc.Balance).
		Str("type", msgType).
		Int("peers", len(peers)).
		Msg("Starting DID propagation to peers")

	// Send message to all peers
	// Create waitgroup for tracking goroutines
	wg, err := DIDLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.DIDForwardThread)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create waitgroup for DID forwarding")
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
				log.Error().Err(err).Str("peer", peerIDForGoroutine.String()).Msg("Failed to open stream for DID")
				return err
			}
			defer stream.Close()

			// Send the message
			_, err = stream.Write(msgBytes)
			if err != nil {
				log.Error().Err(err).Str("peer", peerIDForGoroutine.String()).Msg("Failed to send DID message")
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
			log.Error().Err(err).Str("peer", peerID.String()).Msg("Failed to start goroutine for DID propagation")
		}
	}

	// Wait for all sends to complete
	wg.Wait()

	log.Info().
		Str("msg_id", msg.ID).
		Str("did", doc.DIDAddress).
		Str("public_key", doc.Address.Hex()).
		Str("balance", doc.Balance).
		Str("type", msgType).
		Int("success", successCount).
		Int("total", len(peers)).
		Msg("DID propagation complete")

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
