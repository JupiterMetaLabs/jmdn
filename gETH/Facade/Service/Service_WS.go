package Service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config/GRO"
	"gossipnode/gETH/Facade/Service/Types"
	Utils "gossipnode/gETH/Facade/Service/utils"
	"gossipnode/gETH/common"

	"github.com/JupiterMetaLabs/ion"
	ethcommon "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// Global subscription manager for new heads
var (
	newHeadsSubscriptions = struct {
		sync.RWMutex
		subscribers map[string]*NewHeadsSubscription
		isPolling   bool
	}{
		subscribers: make(map[string]*NewHeadsSubscription),
	}

	// newHeadsSeq numbers subscriptions.
	newHeadsSeq atomic.Uint64

	// newHeadsPollInterval is how often the poller checks for new blocks.
	newHeadsPollInterval = 2 * time.Second

	// activeNewHeadsPollers counts running pollers (at most one).
	activeNewHeadsPollers atomic.Int32

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
	// Create a unique subscription ID (a time-based ID collides under load).
	subscriptionID := fmt.Sprintf("newheads_%d", newHeadsSeq.Add(1))

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

	// Return channel and cleanup function. Idempotent: the caller's stop and
	// the context watcher below may both call it. Removing the entry under the
	// write lock means notifyNewBlock (which sends under the read lock) can
	// never send on the closed channel.
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			newHeadsSubscriptions.Lock()
			delete(newHeadsSubscriptions.subscribers, subscriptionID)
			newHeadsSubscriptions.Unlock()

			cancel()
			close(subscription.Channel)

			logger().Info(context.Background(), "New heads subscription closed", ion.String("subscription_id", subscriptionID))
		})
	}

	// Release the subscription when the caller's context ends even if the
	// caller never calls cleanup, so a dropped connection cannot leave an
	// entry (and the poller) alive forever.
	go func() {
		<-subCtx.Done()
		cleanup()
	}()

	return subscription.Channel, cleanup, nil
}

// startBlockPollerIfNeeded starts the block polling goroutine if it's not already running
func startBlockPollerIfNeeded() {
	newHeadsSubscriptions.Lock()
	if newHeadsSubscriptions.isPolling {
		newHeadsSubscriptions.Unlock()
		return
	}
	newHeadsSubscriptions.isPolling = true
	newHeadsSubscriptions.Unlock()

	BlockPoller, err := common.InitializeGRO(GRO.FacadeLocal)
	if err != nil {
		log.Printf("❌ Failed to initialize local gro: %v", err)
		newHeadsSubscriptions.Lock()
		newHeadsSubscriptions.isPolling = false
		newHeadsSubscriptions.Unlock()
		return
	}

	// Initialize the lastProcessedBlock to the current latest block
	// so we don't scan the entire chain from block 0.
	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initCancel()
	if latest, err := DB_OPs.GetLatestBlockNumber(initCtx, nil); err == nil {
		setLastProcessedBlock(latest)
	}

	// Start polling in a separate goroutine
	BlockPoller.Go(GRO.BlockPollerThread, func(ctx context.Context) error {
		pollForNewBlocks(ctx)
		return nil
	})
}

// pollForNewBlocks continuously polls for new blocks and notifies subscribers
func pollForNewBlocks(ctx context.Context) {
	activeNewHeadsPollers.Add(1)
	defer activeNewHeadsPollers.Add(-1)
	ticker := time.NewTicker(newHeadsPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check if we still have subscribers
			newHeadsSubscriptions.Lock()
			if len(newHeadsSubscriptions.subscribers) == 0 {
				// No more subscribers, stop polling. `return`, not `break`:
				// break only left the select, so the loop kept polling while
				// isPolling said otherwise, and the next subscriber started a
				// second poller.
				newHeadsSubscriptions.isPolling = false
				newHeadsSubscriptions.Unlock()
				return
			}
			newHeadsSubscriptions.Unlock()

			// Get latest block number
			latestBlock, err := DB_OPs.GetLatestBlockNumber(ctx, nil)
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
					if ctx.Err() != nil {
						return
					}
					block, err := getBlockForSubscription(ctx, blockNum)
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
}

// getBlockForSubscription retrieves a block and converts it to Types.Block format
func getBlockForSubscription(ctx context.Context, blockNumber uint64) (*Types.Block, error) {
	// Get the ZK block from database
	zkBlock, err := DB_OPs.ReadZKBlockByNumber(ctx, nil, blockNumber)
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

// logBlockStoreWait bounds how long a subscription holds a log while its
// block is being stored. Apply and store normally finish within a second.
const logBlockStoreWait = 10 * time.Second

// logFateCheckInterval is how often a subscription asks whether the blocks of
// its held logs have been stored.
const logFateCheckInterval = 100 * time.Millisecond

// maxHeldLogs caps the logs one subscription holds, whether their blocks are
// still being stored or the consumer has not taken them yet. A subscription
// that would exceed it is closed, as geth closes a subscription whose buffer
// fills, instead of silently losing logs.
const maxHeldLogs = 65536

// slowConsumerTimeout is how long a subscription waits on a consumer that
// takes nothing while a log is ready for it. Past it the subscription is
// closed rather than holding logs for a client that stopped reading.
const slowConsumerTimeout = 10 * time.Second

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
		defer DB_OPs.GlobalLogWriter.Unsubscribe(rawCh)
		forwardStoredLogs(ctx, rawCh, out, q, storedLogBlockFate, logFateCheckInterval, logBlockStoreWait)
	}()

	cleanup := func() {
		DB_OPs.GlobalLogWriter.Unsubscribe(rawCh)
	}
	return out, cleanup, nil
}

// logBlockFate is what is known about the block a held log belongs to.
type logBlockFate uint8

const (
	logBlockPending logBlockFate = iota // not stored (yet), or unreadable
	logBlockStored                      // the stored block at that height is this block
	logBlockGone                        // a different block is stored at that height
)

type logBlockKey struct {
	height uint64
	hash   ethcommon.Hash
}

// storedLogBlockFate reads the block stored at key.height.
func storedLogBlockFate(key logBlockKey) logBlockFate {
	h, found, err := DB_OPs.StoredBlockHash(key.height)
	switch {
	case err != nil || !found:
		return logBlockPending
	case h == key.hash:
		return logBlockStored
	default:
		return logBlockGone
	}
}

// heldBlock is a run of consecutive held logs from one block.
type heldBlock struct {
	key   logBlockKey
	since time.Time
	fate  logBlockFate
	logs  []*ethtypes.Log
}

// forwardStoredLogs forwards, in arrival order, the logs from rawCh that match
// q and whose block is the block stored at their height.
//
// Logs are fanned out while their block is applied, before it is stored, and
// the fan-out drops a log when a subscriber's channel is full. So this loop
// never blocks on a block's fate: it keeps draining rawCh into a queue of
// per-block runs, asks once per interval about the runs whose block is still
// pending (one read per block, never per log), releases a run once its block
// is stored, and drops it once a different block is stored at that height or
// wait passes with nothing stored.
//
// The subscription ends (out is closed) when the consumer takes nothing for
// slowConsumerTimeout while a log is ready for it, or when more than
// maxHeldLogs are held. Either way a client that stopped reading costs nothing
// afterwards.
func forwardStoredLogs(ctx context.Context, rawCh <-chan *ethtypes.Log, out chan<- Types.Log, q *Types.FilterQuery,
	fate func(logBlockKey) logBlockFate, interval, wait time.Duration) {
	forwardStoredLogsLimits(ctx, rawCh, out, q, fate, interval, wait, maxHeldLogs, slowConsumerTimeout)
}

func forwardStoredLogsLimits(ctx context.Context, rawCh <-chan *ethtypes.Log, out chan<- Types.Log, q *Types.FilterQuery,
	fate func(logBlockKey) logBlockFate, interval, wait time.Duration, maxHeld int, slowTimeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var queue []*heldBlock
	held := 0                // logs in queue
	pending := 0             // runs whose block is not yet known stored or gone
	var readySince time.Time // when the head became sendable and has not been taken since

	resolve := func() {
		if pending == 0 {
			return
		}
		now := time.Now()
		asked := make(map[logBlockKey]logBlockFate, pending)
		gone := false
		for _, b := range queue {
			if b.fate != logBlockPending {
				continue
			}
			f, ok := asked[b.key]
			if !ok {
				f = fate(b.key)
				asked[b.key] = f
			}
			if f == logBlockPending && now.Sub(b.since) >= wait {
				f = logBlockGone
			}
			if f != logBlockPending {
				b.fate = f
				pending--
				gone = gone || f == logBlockGone
			}
		}
		if !gone {
			return
		}
		// Remove runs whose block is gone; the order of the rest is kept.
		kept := queue[:0]
		for _, b := range queue {
			if b.fate == logBlockGone {
				held -= len(b.logs)
				continue
			}
			kept = append(kept, b)
		}
		for i := len(kept); i < len(queue); i++ {
			queue[i] = nil
		}
		queue = kept
	}

	for {
		var sendCh chan<- Types.Log
		var next Types.Log
		if len(queue) > 0 && queue[0].fate == logBlockStored {
			sendCh, next = out, ethLogToTypesLog(queue[0].logs[0])
			if readySince.IsZero() {
				readySince = time.Now()
			}
		} else {
			readySince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return
		case l, ok := <-rawCh:
			if !ok {
				return
			}
			if l == nil || !logMatchesFilter(l, q) {
				continue
			}
			if held >= maxHeld {
				logger().Warn(ctx, "closing log subscription: too many logs held for it", ion.Int("held", held))
				return
			}
			k := logBlockKey{l.BlockNumber, l.BlockHash}
			if n := len(queue); n > 0 && queue[n-1].key == k && queue[n-1].fate != logBlockGone {
				queue[n-1].logs = append(queue[n-1].logs, l)
			} else {
				queue = append(queue, &heldBlock{key: k, since: time.Now(), logs: []*ethtypes.Log{l}})
				pending++
			}
			held++
		case <-ticker.C:
			resolve()
			if !readySince.IsZero() && time.Since(readySince) >= slowTimeout {
				logger().Warn(ctx, "closing log subscription: the client is not reading", ion.Int("held", held))
				return
			}
		case sendCh <- next:
			readySince = time.Time{}
			head := queue[0]
			head.logs[0] = nil
			head.logs = head.logs[1:]
			held--
			if len(head.logs) == 0 {
				queue[0] = nil
				queue = queue[1:]
			}
		}
	}
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
