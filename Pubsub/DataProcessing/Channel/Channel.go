package Channel

import (
	"context"
	"fmt"
	"sync"
	"time"

	Router "gossipnode/Pubsub/Router"
	"gossipnode/config/GRO"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/ion"
)

var (
	ChannelBuffer = make(chan PubSubMessages.GossipMessage, 256) // buffered: decouples producers from the listener (audit NET-02)
	isStarted     bool
	mu            sync.Mutex
)

// AppendMessage is used by producers to push a message into the shared channel.
// It auto-starts the listener if not already running.
func AppendMessage(message *PubSubMessages.GossipMessage) {
	if LocalGRO == nil {
		var err error
		LocalGRO, err = InitializeGRO()
		if err != nil {
			logger().Error(context.Background(), "Error initializing LocalGRO", err)
			return
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !isStarted {
		isStarted = true
		LocalGRO.Go(GRO.PubsubChannelThread, func(ctx context.Context) error {
			startMessageListener()
			return nil
		})
	}

	// Send under mu: closeChannel closes and reassigns ChannelBuffer under the
	// SAME lock, so the send can never race a close (send-on-closed is an
	// uncatchable panic in the stream goroutine — audit NET-02). Non-blocking,
	// so holding mu here cannot deadlock.
	select {
	case ChannelBuffer <- *message:
	default:
		logger().Warn(context.Background(), "Channel buffer full, message dropped")
	}
}

// startMessageListener is an internal helper that runs until idle for >10s.
func startMessageListener() {
	logger().Debug(context.Background(), "Listener started")

	idleTimer := time.NewTimer(10 * time.Second)
	defer idleTimer.Stop()

	for {
		select {
		case msg := <-ChannelBuffer:
			if msg.ID == "" {
				continue
			}

			// Reset idle timer on each message
			if !idleTimer.Stop() {
				<-idleTimer.C
			}
			idleTimer.Reset(10 * time.Second)

			// Process safely
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger().Warn(context.Background(), "Recovered in message handler",
							ion.String("recovery", fmt.Sprintf("%v", r)))
					}
				}()
				processMessage(msg)
			}()

		// NO messages for 10 seconds, close the channel automatically
		case <-idleTimer.C:
			logger().Debug(context.Background(), "Listener idle for 10s, closing channel")
			closeChannel()
			return
		}
	}
}

func closeChannel() {
	// Close + reassign under mu so a concurrent AppendMessage (which sends
	// under the same lock) cannot send on the closed channel (audit NET-02).
	mu.Lock()
	defer mu.Unlock()
	select {
	case <-ChannelBuffer: // drain one if needed
	default:
	}
	close(ChannelBuffer)
	isStarted = false
	ChannelBuffer = make(chan PubSubMessages.GossipMessage, 256) // recreate for next use

	logger().Debug(context.Background(), "Channel closed and reset")
}

func processMessage(msg PubSubMessages.GossipMessage) {
	// This is the to be processed message so Publish message is not a type here
	err := Router.Router(&msg)
	if err != nil {
		logger().Error(context.Background(), "Error processing message", err)
	}
}
