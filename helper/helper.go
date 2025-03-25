package helper

import (
    "encoding/json"
    "github.com/rs/zerolog/log"
    "gossipnode/config"
)

// BroadcastHandler defines an interface for components that can broadcast messages
type BroadcastHandler interface {
    HandleBroadcast(data []byte)
}

// Our global broadcast handler
var broadcastHandler BroadcastHandler

// SetBroadcastHandler sets the broadcast handler for notifications
func SetBroadcastHandler(handler BroadcastHandler) {
    broadcastHandler = handler
}

// NotifyBroadcast sends a notification to the broadcast handler
func NotifyBroadcast(msg config.BlockMessage) {
    // Skip if handler isn't set
    if broadcastHandler == nil {
        log.Debug().Msg("Broadcast handler not set")
        return
    }
    
    // Prepare notification message
    notification := map[string]interface{}{
        "type": msg.Type,
        "data": msg,
    }
    
    // Marshal to JSON
    data, err := json.Marshal(notification)
    if err != nil {
        log.Error().Err(err).Msg("Failed to marshal block notification")
        return
    }
    
    // Send to handler for broadcasting
    broadcastHandler.HandleBroadcast(data)
    log.Debug().Str("block_id", msg.ID).Msg("Block notification sent to broadcaster")
}