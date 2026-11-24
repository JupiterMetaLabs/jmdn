package Channel

import (
	"context"
	"fmt"
	"sync"

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

// startMessageListener runs for the process lifetime. It no longer closes the
// channel on idle: closing + reassigning a shared channel producers still send
// to was the entire send-on-closed / lossy-close / double-close class (audit
// NET-02/NET-07). isStarted already makes startup once-only, so a single
// long-lived listener on a buffered channel is strictly simpler and safe. The
// cost is one parked goroutine when idle — negligible.
func startMessageListener() {
	logger().Debug(context.Background(), "Listener started")

	for msg := range ChannelBuffer {
		if msg.ID == "" {
			continue
		}
		// Process safely — a handler panic must not kill the listener.
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger().Warn(context.Background(), "Recovered in message handler",
						ion.String("recovery", fmt.Sprintf("%v", r)))
				}
			}()
			processMessage(msg)
		}()
	}
}

func processMessage(msg PubSubMessages.GossipMessage) {
	// This is the to be processed message so Publish message is not a type here
	err := Router.Router(&msg)
	if err != nil {
		logger().Error(context.Background(), "Error processing message", err)
	}
}
