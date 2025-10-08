package Service

import (
	"context"
	"fmt"
	"gossipnode/DB_OPs"
	"gossipnode/logging"
	"math/big"
	"time"

	"go.uber.org/zap"
)

func LogData(ctx context.Context, Message string, Function string, status int) error {
	// Create a new context with timeout for logging operation
	logCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	tempLogger := GetLogger()
	if tempLogger == nil || tempLogger.Logger == nil {
		return fmt.Errorf("logger is not initialized")
	}

	switch status {
	case 1:
		// Success
		tempLogger.Logger.Info(Message,
			zap.String(logging.Function, Function),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.Time(logging.Created_at, time.Now()),
		)
	case -1:
		// Error
		tempLogger.Logger.Error(Message,
			zap.String(logging.Function, Function),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.Time(logging.Created_at, time.Now()),
		)
	default:
		return fmt.Errorf("invalid status code: %d", status)
	}

	// Check if context was cancelled
	select {
	case <-logCtx.Done():
		return logCtx.Err()
	default:
		return nil
	}
}

func ChainID(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ChainID := 7000700

	// Log the operation
	if err := LogData(opCtx, "ChainID returned to the client", "ChainID", 1); err != nil {
		// Log error but don't fail the operation
		fmt.Printf("Failed to log ChainID operation: %v\n", err)
	}

	return big.NewInt(int64(ChainID)), nil
}

func ClientVersion(ctx context.Context) (string, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ClientVersion := "JMDT/v1.0.0"

	// Log the operation
	if err := LogData(opCtx, "ClientVersion returned to the client", "ClientVersion", 1); err != nil {
		// Log error but don't fail the operation
		fmt.Printf("Failed to log ClientVersion operation: %v\n", err)
	}

	return ClientVersion, nil
}

func BlockNumber(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Pass the context to the database operation
	BlockNumber, err := DB_OPs.GetLatestBlockNumber(nil)
	if err != nil {
		// Log error
		if logErr := LogData(opCtx, fmt.Sprintf("BlockNumber failed: %v", err), "BlockNumber", -1); logErr != nil {
			fmt.Printf("Failed to log BlockNumber error: %v\n", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := LogData(opCtx, fmt.Sprintf("BlockNumber returned to the client: %d", BlockNumber), "BlockNumber", 1); logErr != nil {
		fmt.Printf("Failed to log BlockNumber success: %v\n", logErr)
	}

	return big.NewInt(int64(BlockNumber)), nil
}

func BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Block, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ZKBlock, err := DB_OPs.GetZKBlockByNumber(nil, num.Uint64())
	if err != nil {
		if logErr := LogData(opCtx, fmt.Sprintf("BlockByNumber failed: %v", err), "BlockByNumber", -1); logErr != nil {
			fmt.Printf("Failed to log BlockByNumber error: %v\n", logErr)
		}
		return nil, err
	}

	
}
