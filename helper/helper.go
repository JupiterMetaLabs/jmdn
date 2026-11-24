package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"gossipnode/config"

	"github.com/JupiterMetaLabs/ion"
	"github.com/holiman/uint256"
)

// BroadcastHandler defines an interface for components that can broadcast messages
type BroadcastHandler interface {
	HandleBroadcast(data []byte)
}

// Our global broadcast handler
var broadcastHandler BroadcastHandler

func ConvertBigToUint256(b *big.Int) (*uint256.Int, bool) {
	u, overflow := uint256.FromBig(b)
	if overflow {
		// Note: Using context.Background() for conversion errors without context
		logger().Error(context.Background(), "Overflow occurred while converting big.Int to uint256",
			errors.New("uint256 overflow"),
			ion.String("value", b.String()))
		return nil, true
	}
	return u, overflow
}

func BigIntToUint64Safe(b *big.Int) (uint64, error) {
	if b.Sign() < 0 {
		return 0, fmt.Errorf("cannot convert negative big.Int to uint64")
	}
	if b.BitLen() > 64 {
		return 0, fmt.Errorf("big.Int too large for uint64")
	}
	return b.Uint64(), nil
}

// NotifyBroadcast sends a notification to the broadcast handler
func NotifyBroadcast(msg config.BlockMessage) {
	ctx := context.Background()
	// Skip if handler isn't set
	if broadcastHandler == nil {
		logger().Debug(ctx, "Broadcast handler not set")
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
		logger().Error(ctx, "Failed to marshal block notification", err)
		return
	}

	// Send to handler for broadcasting
	broadcastHandler.HandleBroadcast(data)
	logger().Debug(ctx, "Block notification sent to broadcaster",
		ion.String("block_id", msg.ID))
}
