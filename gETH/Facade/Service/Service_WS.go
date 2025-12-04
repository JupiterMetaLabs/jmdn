package Service

import (
	AppContext "gossipnode/config/Context"
	"context"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/gETH/Facade/Service/Types"
	Utils "gossipnode/gETH/Facade/Service/utils"
	"sync"
	"time"
)

const(
	ServiceWSAppContext = "gETH.facade.service.ws"
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
	subCtx, cancel := AppContext.GetAppContext(ServiceWSAppContext).NewChildContext()

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
	fmt.Printf("New heads subscription created: %s\n", subscriptionID)

	// Return channel and cleanup function
	cleanup := func() {
		newHeadsSubscriptions.Lock()
		delete(newHeadsSubscriptions.subscribers, subscriptionID)
		newHeadsSubscriptions.Unlock()

		cancel()
		close(subscription.Channel)

		fmt.Printf("New heads subscription closed: %s\n", subscriptionID)
	}

	return subscription.Channel, cleanup, nil
}

// startBlockPollerIfNeeded starts the block polling goroutine if it's not already running
func startBlockPollerIfNeeded() {
	// Check if we already have subscribers and polling is needed
	newHeadsSubscriptions.RLock()
	hasSubscribers := len(newHeadsSubscriptions.subscribers) > 0
	newHeadsSubscriptions.RUnlock()

	if hasSubscribers {
		// Start polling in a separate goroutine
		go pollForNewBlocks()
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
			fmt.Printf("Failed to get latest block number: %v\n", err)
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
					fmt.Printf("Failed to get block %d: %v\n", blockNum, err)
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
				fmt.Printf("Subscriber %s channel full, skipping block %d\n", subscription.ID, block.Header.Number)
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

// SubscribeLogs implements the Service interface - placeholder implementation
func (s *ServiceImpl) SubscribeLogs(ctx context.Context, q *Types.FilterQuery) (<-chan Types.Log, func(), error) {
	// TODO: Implement logs subscription functionality
	ch := make(chan Types.Log, 100)
	cleanup := func() {
		close(ch)
	}
	return ch, cleanup, fmt.Errorf("SubscribeLogs method not yet implemented")
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
