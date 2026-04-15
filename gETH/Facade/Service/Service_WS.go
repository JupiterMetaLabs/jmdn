package Service

import (
	"context"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/config/GRO"
	"gossipnode/gETH/Facade/Service/Types"
	Utils "gossipnode/gETH/Facade/Service/utils"
	"gossipnode/gETH/common"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// Global subscription manager for new heads
var (
	newHeadsSubscriptions = struct {
		sync.RWMutex
		subscribers map[string]*NewHeadsSubscription
	}{
		subscribers: make(map[string]*NewHeadsSubscription),
	}

	// Block polling state
	lastProcessedBlock uint64
	blockMutex         sync.RWMutex
)

// NewHeadsSubscription represents a subscription to new block headers
type NewHeadsSubscription struct {
	ID       string
	Channel  chan *Types.Block
	Context  context.Context
	Cancel   context.CancelFunc
	LastSeen uint64
}

// SubscribeNewHeads creates a subscription to new block headers
func (s *ServiceImpl) SubscribeNewHeads(ctx context.Context) (<-chan *Types.Block, func(), error) {
	// Create a unique subscription ID
	subscriptionID := fmt.Sprintf("newheads_%d", time.Now().UTC().UnixNano())

	// Create context with cancellation
	subCtx, cancel := context.WithCancel(ctx)

	// Create subscription
	subscription := &NewHeadsSubscription{
		ID:       subscriptionID,
		Channel:  make(chan *Types.Block, 100), // Buffered channel to prevent blocking
		Context:  subCtx,
		Cancel:   cancel,
		LastSeen: getLastProcessedBlock(),
	}

	// Register subscription
	newHeadsSubscriptions.Lock()
	newHeadsSubscriptions.subscribers[subscriptionID] = subscription
	newHeadsSubscriptions.Unlock()

	// Start block polling if not already running
	startBlockPollerIfNeeded()

	// Log subscription creation
	logger().Info(context.Background(), "New heads subscription created", ion.String("subscription_id", subscriptionID))

	// Return channel and cleanup function
	cleanup := func() {
		newHeadsSubscriptions.Lock()
		delete(newHeadsSubscriptions.subscribers, subscriptionID)
		newHeadsSubscriptions.Unlock()

		cancel()
		close(subscription.Channel)

		logger().Info(context.Background(), "New heads subscription closed", ion.String("subscription_id", subscriptionID))
	}

	return subscription.Channel, cleanup, nil
}

// startBlockPollerIfNeeded starts the block polling goroutine if it's not already running
func startBlockPollerIfNeeded() {
	BlockPoller, err := common.InitializeGRO(GRO.FacadeLocal)
	if err != nil {
		log.Printf("❌ Failed to initialize local gro: %v", err)
		return
	}
	// Check if we already have subscribers and polling is needed
	newHeadsSubscriptions.RLock()
	hasSubscribers := len(newHeadsSubscriptions.subscribers) > 0
	newHeadsSubscriptions.RUnlock()

	if hasSubscribers {
		// Start polling in a separate goroutine
		BlockPoller.Go(GRO.BlockPollerThread, func(ctx context.Context) error {
			pollForNewBlocks()
			return nil
		})
	}
}

// pollForNewBlocks continuously polls for new blocks and notifies subscribers
func pollForNewBlocks() {
	ticker := time.NewTicker(2 * time.Second) // Poll every 2 seconds
	defer ticker.Stop()

	for range ticker.C {
		// Check if we still have subscribers
		newHeadsSubscriptions.RLock()
		hasSubscribers := len(newHeadsSubscriptions.subscribers) > 0
		newHeadsSubscriptions.RUnlock()

		if !hasSubscribers {
			// No more subscribers, stop polling
			break
		}

		// Get latest block number
		latestBlock, err := DB_OPs.GetLatestBlockNumber(nil)
		if err != nil {
			// Log error but continue polling
			logger().Error(context.Background(), "Failed to get latest block number", err)
			continue
		}

		// Get last processed block
		lastProcessed := getLastProcessedBlock()

		// Check for new blocks
		if latestBlock > lastProcessed {
			// Process new blocks
			for blockNum := lastProcessed + 1; blockNum <= latestBlock; blockNum++ {
				block, err := getBlockForSubscription(blockNum)
				if err != nil {
					// Log error but continue with other blocks
					logger().Error(context.Background(), "Failed to get block", err, ion.Int("block_num", int(blockNum)))
					continue
				}

				// Notify all subscribers
				notifyNewBlock(block)

				// Update last processed block
				setLastProcessedBlock(blockNum)
			}
		}
	}
}

// getBlockForSubscription retrieves a block and converts it to Types.Block format
func getBlockForSubscription(blockNumber uint64) (*Types.Block, error) {
	// Get the ZK block from database
	zkBlock, err := DB_OPs.GetZKBlockByNumber(nil, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get ZK block %d: %w", blockNumber, err)
	}

	// Convert to Types.Block format
	block := Utils.ConvertZKBlockToBlock(zkBlock)

	return block, nil
}

// notifyNewBlock sends a new block to all active subscribers
func notifyNewBlock(block *Types.Block) {
	newHeadsSubscriptions.RLock()
	defer newHeadsSubscriptions.RUnlock()

	for _, subscription := range newHeadsSubscriptions.subscribers {
		// Check if subscription is still active
		select {
		case <-subscription.Context.Done():
			// Subscription is cancelled, skip
			continue
		default:
			// Send block to subscriber
			select {
			case subscription.Channel <- block:
				// Successfully sent
				subscription.LastSeen = block.Header.Number
			default:
				// Channel is full, subscriber is too slow
				// We could close the subscription here, but for now just skip
				logger().Warn(context.Background(), "Subscriber channel full, skipping block", ion.String("subscriber_id", subscription.ID), ion.Int("block_number", int(block.Header.Number)))
			}
		}
	}
}

// getLastProcessedBlock returns the last processed block number
func getLastProcessedBlock() uint64 {
	blockMutex.RLock()
	defer blockMutex.RUnlock()
	return lastProcessedBlock
}

// setLastProcessedBlock sets the last processed block number
func setLastProcessedBlock(blockNumber uint64) {
	blockMutex.Lock()
	defer blockMutex.Unlock()
	lastProcessedBlock = blockNumber
}

// GetActiveSubscriptionsCount returns the number of active new heads subscriptions
func GetActiveSubscriptionsCount() int {
	newHeadsSubscriptions.RLock()
	defer newHeadsSubscriptions.RUnlock()
	return len(newHeadsSubscriptions.subscribers)
}

// SubscribeLogs implements the Service interface.
// It attaches to the GlobalLogWriter fan-out channel and forwards logs that
// match the supplied FilterQuery (address + topic filters) to the caller.
// When ctx is cancelled or the caller invokes the returned cleanup func,
// the subscription is torn down.
func (s *ServiceImpl) SubscribeLogs(ctx context.Context, q *Types.FilterQuery) (<-chan Types.Log, func(), error) {
	// Subscribe to the EVM log fan-out
	rawCh := DB_OPs.GlobalLogWriter.Subscribe()

	// Output channel for the WebSocket forwarder
	out := make(chan Types.Log, 100)

	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				DB_OPs.GlobalLogWriter.Unsubscribe(rawCh)
				return
			case ethLog, ok := <-rawCh:
				if !ok {
					return
				}
				if !logMatchesFilter(ethLog, q) {
					continue
				}
				// Convert go-ethereum types.Log -> Service Types.Log
				tl := ethLogToTypesLog(ethLog)
				select {
				case out <- tl:
				case <-ctx.Done():
					DB_OPs.GlobalLogWriter.Unsubscribe(rawCh)
					return
				}
			}
		}
	}()

	cleanup := func() {
		DB_OPs.GlobalLogWriter.Unsubscribe(rawCh)
	}
	return out, cleanup, nil
}

// logMatchesFilter returns true if ethLog satisfies the address and topic
// constraints in q.  An empty q matches everything.
func logMatchesFilter(l *ethtypes.Log, q *Types.FilterQuery) bool {
	if q == nil {
		return true
	}

	// Address filter (any-of)
	if len(q.Addresses) > 0 {
		addr := strings.ToLower(l.Address.Hex())
		matched := false
		for _, a := range q.Addresses {
			if strings.EqualFold(a, addr) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Topic filter (positional AND, within each position OR, nil = wildcard)
	for pos, orSet := range q.Topics {
		if len(orSet) == 0 {
			continue // wildcard for this position
		}
		if pos >= len(l.Topics) {
			return false // log doesn't have enough topics
		}
		topicHex := l.Topics[pos].Hex()
		posMatched := false
		for _, want := range orSet {
			if strings.EqualFold(want, topicHex) {
				posMatched = true
				break
			}
		}
		if !posMatched {
			return false
		}
	}

	return true
}

// ethLogToTypesLog converts a go-ethereum *types.Log to the Service Types.Log
// used by the WebSocket forwarder.
func ethLogToTypesLog(l *ethtypes.Log) Types.Log {
	topics := make([][]byte, len(l.Topics))
	for i, t := range l.Topics {
		copy := t // avoid loop variable aliasing
		topics[i] = copy[:]
	}
	return Types.Log{
		Address:     l.Address.Bytes(),
		Topics:      topics,
		Data:        l.Data,
		BlockNumber: l.BlockNumber,
		BlockHash:   l.BlockHash.Bytes(),
		TxIndex:     uint64(l.TxIndex),
		TxHash:      l.TxHash.Bytes(),
		LogIndex:    uint64(l.Index),
		Removed:     l.Removed,
	}
}

// SubscribePendingTxs implements the Service interface - placeholder implementation
func (s *ServiceImpl) SubscribePendingTxs(ctx context.Context) (<-chan string, func(), error) {
	// TODO: Implement pending transactions subscription functionality
	ch := make(chan string, 100)
	cleanup := func() {
		close(ch)
	}
	return ch, cleanup, fmt.Errorf("SubscribePendingTxs method not yet implemented")
}
