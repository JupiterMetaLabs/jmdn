package Channel

import (
	"context"
	"fmt"
	Router "gossipnode/Pubsub/Router"
	"gossipnode/config/GRO"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"sync"
	"time"
)

var (
	ChannelBuffer = make(chan PubSubMessages.GossipMessage) // small buffer prevents blocking
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
			fmt.Println("Error initializing LocalGRO:", err)
			return
		}
	}
	mu.Lock()
	if !isStarted {
		isStarted = true
		LocalGRO.Go(GRO.PubsubChannelThread, func(ctx context.Context) error {
			startMessageListener()
			return nil
		})
	}
	mu.Unlock()

	select {
	case ChannelBuffer <- *message:
	default:
		fmt.Println("⚠️ Channel buffer full, message dropped")
	}
}

// startMessageListener is an internal helper that runs until idle for >10s.
func startMessageListener() {
	fmt.Println("▶️ Listener started")

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
						fmt.Println("Recovered in message handler:", r)
					}
				}()
				processMessage(msg)
			}()

		// NO messages for 10 seconds, close the channel automatically
		case <-idleTimer.C:
			fmt.Println("⏹️ Listener idle for 10s, closing channel")
			closeChannel()
			return
		}
	}
}

func closeChannel() {
	select {
	case <-ChannelBuffer: // drain one if needed
	default:
	}
	defer func() {
		recover() // ignore panic if already closed
	}()

	close(ChannelBuffer)
	isStarted = false
	ChannelBuffer = make(chan PubSubMessages.GossipMessage) // recreate new channel for next use

	fmt.Println("✅ Channel closed and reset")
}

func processMessage(msg PubSubMessages.GossipMessage) {
	// This is the to be processed message so Publish message is not a type here
	err := Router.Router(&msg)
	if err != nil {
		fmt.Println("Error processing message:", err)
	}
}
