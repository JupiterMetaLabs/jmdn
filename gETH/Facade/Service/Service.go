package Service

import (
	"context"
	"encoding/json"
	"fmt"
	block "gossipnode/Block"
	"gossipnode/gETH/Facade/Service/Logger"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/gETH/Facade/Service/Utils"
	"math/big"
	"time"
)

func ChainID(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ChainID := 7000700

	// Log the operation
	if err := Logger.LogData(opCtx, "ChainID returned to the client", "ChainID", 1); err != nil {
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
	if err := Logger.LogData(opCtx, "ClientVersion returned to the client", "ClientVersion", 1); err != nil {
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
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockNumber failed: %v", err), "BlockNumber", -1); logErr != nil {
			fmt.Printf("Failed to log BlockNumber error: %v\n", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockNumber returned to the client: %d", BlockNumber), "BlockNumber", 1); logErr != nil {
		fmt.Printf("Failed to log BlockNumber success: %v\n", logErr)
	}

	return big.NewInt(int64(BlockNumber)), nil
}

func BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Types.Block, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ZKBlock, err := DB_OPs.GetZKBlockByNumber(nil, num.Uint64())
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber failed: %v", err), "BlockByNumber", -1); logErr != nil {
			fmt.Printf("Failed to log BlockByNumber error: %v\n", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber returned to the client: %d", ZKBlock.BlockNumber), "BlockByNumber", 1); logErr != nil {
		fmt.Printf("Failed to log BlockByNumber success: %v\n", logErr)
	}

	// Convert the ZKBlock from GetZKBlockByNumber to Block
	block := Utils.ConvertZKBlockToBlock(ZKBlock)
	if block == nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber failed: %v", err), "BlockByNumber", -1); logErr != nil {
			fmt.Printf("Failed to log BlockByNumber error: %v\n", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber returned to the client: %d", ZKBlock.BlockNumber), "BlockByNumber", 1); logErr != nil {
		fmt.Printf("Failed to log BlockByNumber success: %v\n", logErr)
	}

	return block, nil
}


// Need to add more functionality to this
func Balance(ctx context.Context, addr string, block *big.Int) (*big.Int, error) {	

	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Lets assume block is the latest - so we will get the balance from the latest block
	// Future we will add the balance retrival based on the particular block. 
	AccountDetails, err := DB_OPs.GetAccount(nil, Utils.ConvertAddress(addr))
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance failed: %v", err), "Balance", -1); logErr != nil {
			fmt.Printf("Failed to log Balance error: %v\n", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance returned to the client: %s", AccountDetails.Balance), "Balance", 1); logErr != nil {
		fmt.Printf("Failed to log Balance success: %v\n", logErr)
	}

	// Convert the balance from string to big.Int
	balance, err := Utils.ConvertBalance(AccountDetails.Balance)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance failed: %v", err), "Balance", -1); logErr != nil {
			fmt.Printf("Failed to log Balance error: %v\n", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance returned to the client: %s", AccountDetails.Balance), "Balance", 1); logErr != nil {
		fmt.Printf("Failed to log Balance success: %v\n", logErr)
	}

	return balance, nil
}

func SendRawTx(ctx context.Context, rawHex string) (string, error) {

	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Convert the bytes rawHex to proper datastructure and submit the transaction
	var tx config.Transaction
	err := json.Unmarshal([]byte(rawHex), &tx)
	if err != nil {
		return "", err
	}

	hash, err := block.SubmitRawTransaction(&tx)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx failed: %v", err), "SendRawTx", -1); logErr != nil {
			fmt.Printf("Failed to log SendRawTx error: %v\n", logErr)
		}
		return "", err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx returned to the client: %s", hash), "SendRawTx", 1); logErr != nil {
		fmt.Printf("Failed to log SendRawTx success: %v\n", logErr)
	}

	return hash, nil
}

func TxByHash(ctx context.Context, hash string) (*Types.Tx, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Pass the context to the database operation
	ZKTx, err := DB_OPs.GetTransactionByHash(nil, hash)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash failed: %v", err), "TxByHash", -1); logErr != nil {
			fmt.Printf("Failed to log TxByHash error: %v\n", logErr)
		}
		return nil, err
	}

	// Convert the ZKTx from GetTransactionByHash to Tx
	tx := Utils.ConvertTrabsactionToTx(ZKTx)
	if tx == nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash failed: %v", err), "TxByHash", -1); logErr != nil {
			fmt.Printf("Failed to log TxByHash error: %v\n", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash returned to the client: %s", hash), "TxByHash", 1); logErr != nil {
		fmt.Printf("Failed to log TxByHash success: %v\n", logErr)
	}

	return tx, nil
}

