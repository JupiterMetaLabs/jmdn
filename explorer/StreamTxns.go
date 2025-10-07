package explorer

import (
	"encoding/json"
	"sync"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/gin-gonic/gin"
)

// StreamEvent represents a single SSE event
type StreamEvent struct {
	EventType string      `json:"event"`
	Data      interface{} `json:"data"`
}

// streamRegistry keeps track of all active connections
var streamRegistry = struct {
	sync.Mutex
	clients map[chan string]struct{}
}{
	clients: make(map[chan string]struct{}),
}

// BroadcastNewBlock sends new block data to all connected clients
func BroadcastNewBlock(block *config.ZKBlock) {
	event := StreamEvent{
		EventType: "new_block",
		Data:      block,
	}
	sendEventToClients(event)
}

// BroadcastNewTransaction sends new transaction data to all connected clients
func BroadcastNewTransaction(tx *config.Transaction) {
	event := StreamEvent{
		EventType: "new_transaction",
		Data:      tx,
	}
	sendEventToClients(event)
}

func sendEventToClients(event StreamEvent) {
	streamRegistry.Lock()
	defer streamRegistry.Unlock()

	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	for client := range streamRegistry.clients {
		select {
		case client <- string(data):
		default:
			// Client is too slow, remove them
			close(client)
			delete(streamRegistry.clients, client)
		}
	}
}

// streamBlocks handles SSE connections for block and transaction streaming
func (s *ImmuDBServer) streamBlocks(c *gin.Context) {
	// Create a channel for this client
	messageChan := make(chan string)

	// Add this client to the registry
	streamRegistry.Lock()
	streamRegistry.clients[messageChan] = struct{}{}
	streamRegistry.Unlock()

	// Remove this client when the connection closes
	defer func() {
		streamRegistry.Lock()
		if _, ok := streamRegistry.clients[messageChan]; ok {
			close(messageChan)
			delete(streamRegistry.clients, messageChan)
		}
		streamRegistry.Unlock()
	}()

	// Listen to connection close and un-register messageChan
	notify := c.Writer.CloseNotify()

	// Send initial data (e.g., latest block)
	latestBlock, err := DB_OPs.GetLatestBlockNumber(&s.defaultdb)
	if err == nil {
		block, err := DB_OPs.GetZKBlockByNumber(&s.defaultdb, latestBlock)
		if err == nil {
			event := StreamEvent{
				EventType: "initial_block",
				Data:      block,
			}
			data, _ := json.Marshal(event)
			c.SSEvent("message", string(data))
			c.Writer.Flush()
		}
	}

	// Keep the connection alive and stream events
	for {
		select {
		case <-notify:
			// Client disconnected
			return
		case msg := <-messageChan:
			// Write to the ResponseWriter
			c.SSEvent("message", msg)
			c.Writer.Flush()
		}
	}
}
