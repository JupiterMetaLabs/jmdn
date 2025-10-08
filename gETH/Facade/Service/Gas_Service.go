package Service

import (
	"context"
	"fmt"
	block "gossipnode/Block"
	"time"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/gETH/Facade/Service/Logger"
)

func GasPrice(ctx context.Context, msg Types.CallMsg) (uint64, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get the mempool client
	mempoolClient, err := block.ReturnMempoolObject()
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("EstimateGas failed: %v", err), "EstimateGas", -1); logErr != nil {
			fmt.Printf("Failed to log EstimateGas error: %v\n", logErr)
		}
		return 0, err
	}

	// Get the Fee Stats
	feeStats, err := mempoolClient.WrapperGetFeeStatistics()
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("Estimate Gas Fee Stats failed: %v", err), "EstimateGas", -1); logErr != nil {
			fmt.Printf("Failed to log EstimateGas error: %v\n", logErr)
		}
		return 0, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("EstimateGas returned to the client: %d", feeStats.RecommendedFees.Standard), "EstimateGas", 1); logErr != nil {
		fmt.Printf("Failed to log EstimateGas success: %v\n", logErr)
	}
	return feeStats.RecommendedFees.Standard, nil
}

func EstimateGas(ctx context.Context, msg Types.CallMsg) (uint64, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Base gas cost for any transaction
	baseGas := uint64(21000)

	// Additional gas for contract deployment
	if msg.To == "" {
		baseGas += 32000 // Contract creation cost
	}

	// Additional gas for data payload
	if len(msg.Data) > 0 {
		// Calculate gas for data
		// - 4 gas for each zero byte
		// - 16 gas for each non-zero byte
		var dataGas uint64
		for _, b := range msg.Data {
			if b == 0 {
				dataGas += 4
			} else {
				dataGas += 16
			}
		}
		baseGas += dataGas
	}

	// Additional gas for value transfer
	if msg.Value != nil && msg.Value.Sign() > 0 {
		baseGas += 9000 // Value transfer cost
	}

	// Add a buffer for safety (5%)
	estimatedGas := baseGas + (baseGas / 5)

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("EstimateGas returned to client: %d", estimatedGas), "EstimateGas", 1); logErr != nil {
		fmt.Printf("Failed to log EstimateGas success: %v\n", logErr)
	}

	return estimatedGas, nil
}