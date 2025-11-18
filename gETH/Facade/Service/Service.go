package Service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	block "gossipnode/Block"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Logger"
	"gossipnode/gETH/Facade/Service/Types"
	Utils "gossipnode/gETH/Facade/Service/utils"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// ServiceImpl implements the Service interface
type ServiceImpl struct {
	ChainIDValue int
}

// NewService creates a new service implementation
func NewService(chainID int) Service {
	return &ServiceImpl{
		ChainIDValue: chainID,
	}
}

func (s *ServiceImpl) ChainID(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Log the operation
	if err := Logger.LogData(opCtx, "ChainID returned to the client", "ChainID", 1); err != nil {
		// Log error but don't fail the operation
		fmt.Printf("Failed to log ChainID operation: %v\n", err)
	}

	return big.NewInt(int64(s.ChainIDValue)), nil
}

func (s *ServiceImpl) ClientVersion(ctx context.Context) (string, error) {
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

func (s *ServiceImpl) BlockNumber(ctx context.Context) (*big.Int, error) {
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

func (s *ServiceImpl) GetTransactionCount(ctx context.Context, addr string, block string) (*big.Int, error) {
	// Create a new context with timeout for this operation
	_, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	address := Utils.ConvertAddress(addr)
	Transactions, err := DB_OPs.GetTransactionsByAccount(nil, &address)
	// Return the transaction count for the given address of latest block
	// Transactions, err := DB_OPs.CountTransactions(nil)
	if err != nil {
		return nil, err
	}

	// fmt.Println("Transactions: ", Transactions)
	// Convert the Transactions to big.Int
	// TransactionsBigInt := big.NewInt(int64(Transactions))

	// return TransactionsBigInt, nil
	return big.NewInt(int64(len(Transactions))), nil
}

func (s *ServiceImpl) BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Types.Block, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
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
func (s *ServiceImpl) Balance(ctx context.Context, addr string, block *big.Int, network string) (*big.Int, error) {

	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Lets assume block is the latest - so we will get the balance from the latest block
	// Future we will add the balance retrival based on the particular block.
	convertedAddr := Utils.ConvertAddressCaseInsensitive(addr)
	fmt.Printf("DEBUG: Original address: %s, Converted address: %s\n", addr, convertedAddr.Hex())
	AccountDetails, err := DB_OPs.GetAccount(nil, convertedAddr)
	if err != nil {
		fmt.Printf("DEBUG: GetAccount error: %v\n", err)
		fmt.Printf("DEBUG: Error type: %T\n", err)
		// If account not found, create a new account with zero balance
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
			// Convert address to common.Address using case-insensitive conversion
			address := Utils.ConvertAddressCaseInsensitive(addr)

			// Create new account with zero balance
			// We need to provide a DID address, so we'll use the address as DID for now
			didAddress := fmt.Sprintf("%s%s:%s", DB_OPs.DIDPrefix, network, address.Hex())

			// Create the Utils.DIDDoc
			didDoc := Utils.DIDDoc{
				Address:    address,
				DIDAddress: didAddress,
				Metadata:   nil,
			}

			// Create the account and propagate the DID
			if err := Utils.CreateAccountandPropagateDID(didDoc); err != nil {
				if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance failed to create account and propagate DID: %v", err), "Balance", -1); logErr != nil {
					fmt.Printf("Failed to log Balance account creation and propagation error: %v\n", logErr)
				}
				return nil, err
			}

			// Log account creation
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance created new account for address: %s", addr), "Balance", 1); logErr != nil {
				fmt.Printf("Failed to log Balance account creation: %v\n", logErr)
			}

			// Return zero balance for new account
			return big.NewInt(0), nil
		}

		// For other errors, log and return
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance failed: %v", err), "Balance", -1); logErr != nil {
			fmt.Printf("Failed to log Balance error: %v\n", logErr)
		}
		return nil, err
	}

	// Debug: Print account details
	fmt.Printf("DEBUG: Account found - Balance: %s, Address: %s, DID: %s\n", AccountDetails.Balance, AccountDetails.Address.Hex(), AccountDetails.DIDAddress)

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

func (s *ServiceImpl) SendRawTx(ctx context.Context, rawHex string) (string, error) {
	// Debugging
	// fmt.Println(">>>>>> SendRawTx received: ", rawHex)
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Remove 0x prefix if present
	rawHex = strings.TrimPrefix(rawHex, "0x")

	// Decode hex string to bytes
	rawBytes, err := hex.DecodeString(rawHex)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx failed to decode hex: %v", err), "SendRawTx", -1); logErr != nil {
			fmt.Printf("Failed to log SendRawTx hex decode error: %v\n", logErr)
		}
		return "", fmt.Errorf("failed to decode hex string: %w", err)
	}

	// Try to parse as JSON first (for test compatibility)
	var tx config.Transaction
	err = json.Unmarshal(rawBytes, &tx)
	if err != nil {
		// If JSON parsing fails, try to parse as RLP-encoded transaction
		fmt.Println(">>>>>> JSON parsing failed, trying RLP parsing")

		// Parse RLP-encoded transaction
		var ethTx types.Transaction
		err = rlp.DecodeBytes(rawBytes, &ethTx)
		if err != nil {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx failed to parse RLP transaction: %v", err), "SendRawTx", -1); logErr != nil {
				fmt.Printf("Failed to log SendRawTx RLP parse error: %v\n", logErr)
			}
			return "", fmt.Errorf("failed to parse RLP transaction: %w", err)
		}

		// Convert Ethereum transaction to our config.Transaction format
		tx = convertEthTxToConfigTx(&ethTx)
		fmt.Println(">>>>>> Converted RLP transaction: ", tx)
	} else {
		fmt.Println(">>>>>> JSON transaction parsed: ", tx)
	}

	hash, err := block.SubmitRawTransaction(&tx)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx failed: %v", err), "SendRawTx", -1); logErr != nil {
			fmt.Printf("Failed to log SendRawTx error: %v\n", logErr)
		}
		// Debugging
		fmt.Println(">>>>>> SubmitRawTransaction failed: ", err)
		return "", err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx returned to the client: %s", hash), "SendRawTx", 1); logErr != nil {
		fmt.Printf("Failed to log SendRawTx success: %v\n", logErr)
	}
	// Debugging
	fmt.Println(">>>>>> SubmitRawTransaction success: ", hash)

	return hash, nil
}

// convertEthTxToConfigTx converts an Ethereum transaction to our config.Transaction format
func convertEthTxToConfigTx(ethTx *types.Transaction) config.Transaction {
	// Get the sender address
	from, _ := types.Sender(types.NewEIP155Signer(ethTx.ChainId()), ethTx)

	// Convert to our transaction format
	tx := config.Transaction{
		Hash:      ethTx.Hash(),
		From:      &from,
		To:        ethTx.To(),
		Value:     ethTx.Value(),
		Type:      uint8(ethTx.Type()),
		Timestamp: uint64(time.Now().UTC().Unix()),
		ChainID:   ethTx.ChainId(),
		Nonce:     ethTx.Nonce(),
		GasLimit:  ethTx.Gas(),
		Data:      ethTx.Data(),
	}

	// Set gas price based on transaction type
	if ethTx.Type() == types.LegacyTxType {
		tx.GasPrice = ethTx.GasPrice()
	} else if ethTx.Type() == types.AccessListTxType {
		tx.GasPrice = ethTx.GasPrice()
	} else if ethTx.Type() == types.DynamicFeeTxType {
		tx.MaxFee = ethTx.GasFeeCap()
		tx.MaxPriorityFee = ethTx.GasTipCap()
	}

	// Set signature components
	v, r, s := ethTx.RawSignatureValues()
	tx.V = v
	tx.R = r
	tx.S = s

	// Debugging
	fmt.Println("Hash: ", tx.Hash.Hex())
	fmt.Println("From: ", tx.From.Hex())
	fmt.Println("To: ", tx.To.Hex())
	fmt.Println("Value: ", tx.Value.String())
	fmt.Println("Type: ", tx.Type)
	fmt.Println("Timestamp: ", tx.Timestamp)
	fmt.Println("ChainID: ", tx.ChainID.String())
	fmt.Println("Nonce: ", tx.Nonce)
	fmt.Println("GasLimit: ", tx.GasLimit)
	fmt.Println("GasPrice: ", tx.GasPrice.String())
	fmt.Println("MaxFee: ", tx.MaxFee.String())
	fmt.Println("MaxPriorityFee: ", tx.MaxPriorityFee.String())
	fmt.Println("Data: ", tx.Data)
	fmt.Println("AccessList: ", tx.AccessList)
	fmt.Println("V: ", tx.V.String())
	fmt.Println("R: ", tx.R.String())
	fmt.Println("S: ", tx.S.String())

	return tx
}

func (s *ServiceImpl) TxByHash(ctx context.Context, hash string) (*Types.Tx, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Normalize hash - ensure it has 0x prefix (keys are stored with 0x prefix)
	normalizedHash := hash
	if !strings.HasPrefix(strings.ToLower(hash), "0x") {
		normalizedHash = "0x" + hash
	}

	// Get the block containing this transaction
	block, err := DB_OPs.GetTransactionBlock(nil, normalizedHash)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash failed to get block: %v", err), "TxByHash", -1); logErr != nil {
			fmt.Printf("Failed to log TxByHash error: %v\n", logErr)
		}
		return nil, err
	}

	// Get the transaction
	ZKTx, err := DB_OPs.GetTransactionByHash(nil, normalizedHash)
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

	// Find the transaction index in the block
	var txIndex *uint64
	for i := range block.Transactions {
		TempBlockHash := block.Transactions[i].Hash.Hex() // Hex() returns with 0x prefix
		if TempBlockHash == normalizedHash {
			idx := uint64(i)
			txIndex = &idx
			break
		}
	}

	// Populate block information
	blockNumber := block.BlockNumber
	tx.BlockNumber = &blockNumber
	tx.BlockHash = block.BlockHash.Bytes()
	tx.TransactionIndex = txIndex

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash returned to the client: %s", hash), "TxByHash", 1); logErr != nil {
		fmt.Printf("Failed to log TxByHash success: %v\n", logErr)
	}

	return tx, nil
}

func (s *ServiceImpl) ReceiptByHash(ctx context.Context, hash string) (map[string]any, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get the receipt from the database
	receipt, err := DB_OPs.GetReceiptByHash(nil, hash)
	if err != nil {
		// Check if error is "transaction not found"
		if err.Error() == "transaction not found" {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash: transaction not found: %s", hash), "ReceiptByHash", -1); logErr != nil {
				fmt.Printf("Failed to log ReceiptByHash error: %v\n", logErr)
			}
			// Return error that will be formatted as JSON-RPC error with code -32000
			return nil, fmt.Errorf("transaction not found")
		}
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash failed: %v", err), "ReceiptByHash", -1); logErr != nil {
			fmt.Printf("Failed to log ReceiptByHash error: %v\n", logErr)
		}
		return nil, err
	}

	// If receipt is nil and no error, it means tx_processing was -1
	// Return nil to indicate result should be null in JSON-RPC response
	if receipt == nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash: tx_processing=-1 for %s, returning null", hash), "ReceiptByHash", 1); logErr != nil {
			fmt.Printf("Failed to log ReceiptByHash: %v\n", logErr)
		}
		return nil, nil
	}

	// Get the transaction to extract from and to addresses
	tx, txErr := DB_OPs.GetTransactionByHash(nil, hash)
	if txErr != nil {
		// Log but don't fail - we can still return receipt without from/to
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash: failed to get transaction for from/to: %v", txErr), "ReceiptByHash", -1); logErr != nil {
			fmt.Printf("Failed to log: %v\n", logErr)
		}
	}

	// Convert logs to JSON-RPC format
	logs := make([]map[string]any, len(receipt.Logs))
	for i, log := range receipt.Logs {
		topics := make([]string, len(log.Topics))
		for j, topic := range log.Topics {
			topics[j] = topic.Hex() // Already has 0x prefix
		}

		logs[i] = map[string]any{
			"address":          log.Address.Hex(), // Already has 0x prefix
			"topics":           topics,
			"data":             "0x" + fmt.Sprintf("%x", log.Data),
			"blockNumber":      "0x" + fmt.Sprintf("%x", log.BlockNumber),
			"transactionHash":  log.TxHash.Hex(), // Already has 0x prefix
			"transactionIndex": "0x" + fmt.Sprintf("%x", log.TxIndex),
			"blockHash":        log.BlockHash.Hex(), // Already has 0x prefix
			"logIndex":         "0x" + fmt.Sprintf("%x", log.LogIndex),
			"removed":          log.Removed,
		}
	}

	// Convert the receipt to a map for JSON serialization in JSON-RPC format
	receiptMap := map[string]any{
		"transactionHash":   receipt.TxHash.Hex(), // Already has 0x prefix
		"transactionIndex":  "0x" + fmt.Sprintf("%x", receipt.TransactionIndex),
		"blockHash":         receipt.BlockHash.Hex(), // Already has 0x prefix
		"blockNumber":       "0x" + fmt.Sprintf("%x", receipt.BlockNumber),
		"cumulativeGasUsed": "0x" + fmt.Sprintf("%x", receipt.CumulativeGasUsed),
		"gasUsed":           "0x" + fmt.Sprintf("%x", receipt.GasUsed),
		"contractAddress":   nil,
		"logs":              logs,
		"logsBloom":         "0x" + fmt.Sprintf("%x", receipt.LogsBloom),
		"status":            "0x" + fmt.Sprintf("%x", receipt.Status),
	}

	// Add transaction type (from receipt or transaction, default to "0x0")
	txType := receipt.Type
	if txType == 0 && tx != nil {
		txType = uint8(tx.Type)
	}
	receiptMap["type"] = "0x" + fmt.Sprintf("%x", txType)

	// Add effectiveGasPrice from transaction
	if tx != nil {
		var effectiveGasPrice *big.Int
		if tx.GasPrice != nil {
			// For legacy (Type 0) and EIP-2930 (Type 1), use GasPrice
			effectiveGasPrice = tx.GasPrice
		} else if tx.MaxFee != nil {
			// For EIP-1559 (Type 2), use MaxFee as effectiveGasPrice
			// In a full implementation, this would be min(MaxFee, baseFee + MaxPriorityFee)
			// but for simplicity, we use MaxFee
			effectiveGasPrice = tx.MaxFee
		}

		if effectiveGasPrice != nil {
			receiptMap["effectiveGasPrice"] = "0x" + effectiveGasPrice.Text(16)
		}
	}

	// Add from and to addresses from transaction
	if tx != nil {
		if tx.From != nil {
			receiptMap["from"] = tx.From.Hex() // Already has 0x prefix
		}
		if tx.To != nil {
			receiptMap["to"] = tx.To.Hex() // Already has 0x prefix
		} else {
			receiptMap["to"] = nil
		}
	}

	// Add contract address if present
	if receipt.ContractAddress != nil {
		receiptMap["contractAddress"] = receipt.ContractAddress.Hex() // Already has 0x prefix
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash returned to the client: %s", hash), "ReceiptByHash", 1); logErr != nil {
		fmt.Printf("Failed to log ReceiptByHash success: %v\n", logErr)
	}

	return receiptMap, nil
}

func (s *ServiceImpl) GetLogs(ctx context.Context, q Types.FilterQuery) ([]Types.Log, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get the logs from the database
	logs, err := DB_OPs.GetLogs(nil, q)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("GetLogs failed: %v", err), "GetLogs", -1); logErr != nil {
			fmt.Printf("Failed to log GetLogs error: %v\n", logErr)
		}
		return nil, err
	}

	return logs, nil
}

// Call implements the Service interface - placeholder implementation
func (s *ServiceImpl) Call(ctx context.Context, msg Types.CallMsg, block *big.Int) ([]byte, error) {
	// TODO: Implement contract call functionality
	return nil, fmt.Errorf("Call method not yet implemented")
}

// EstimateGas UNITS!! implements the Service interface - estimates gas needed for a transaction
func (s *ServiceImpl) EstimateGas(ctx context.Context, msg Types.CallMsg) (uint64, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Base gas cost for any transaction
	baseGas := uint64(21000)

	// // Get fee statistics from routing service to adjust base gas estimate
	// feeStats, err := block.GetFeeStatisticsFromRouting()
	// if err == nil && feeStats != nil {
	// 	fmt.Printf("📊 Fee stats from routing service:\n")
	// 	fmt.Printf("   MeanFee: %d wei (%.9f gwei)\n", feeStats.MeanFee, float64(feeStats.MeanFee)/1000000000.0)
	// 	fmt.Printf("   Standard: %d wei (%.9f gwei)\n", feeStats.RecommendedFees.Standard, float64(feeStats.RecommendedFees.Standard)/1000000000.0)
	// 	fmt.Printf("   Min: %d wei, Max: %d wei, Median: %d wei\n", feeStats.MinFee, feeStats.MaxFee, feeStats.MedianFee)

	// 	if feeStats.MeanFee > 0 {
	// 		// Use mean fee from routing service to adjust base gas
	// 		// Higher fees typically correlate with more complex transactions requiring more gas
	// 		feeMultiplier := float64(feeStats.MeanFee) / 35000000000.0 // Normalize against 35 gwei
	// 		if feeMultiplier > 1.0 {
	// 			fmt.Printf("💰 Applying fee multiplier: %.4f (MeanFee exceeds 35 gwei threshold)\n", feeMultiplier)
	// 			baseGas = uint64(float64(baseGas) * feeMultiplier)
	// 		} else {
	// 			fmt.Printf("✅ Fee multiplier not applied (MeanFee=%.9f gwei < 35 gwei threshold)\n", float64(feeStats.MeanFee)/1000000000.0)
	// 		}
	// 	}
	// } else if err != nil {
	// 	fmt.Printf("⚠️ Failed to get fee statistics from routing service: %v\n", err)
	// }

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
	// if msg.Value != nil && msg.Value.Sign() > 0 {
	// 	baseGas += 9000 // Value transfer cost
	// }

	// Add a buffer for safety (5%)
	estimatedGas := baseGas + (baseGas * 5 / 100)

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("EstimateGas returned to client: %d", estimatedGas), "EstimateGas", 1); logErr != nil {
		fmt.Printf("Failed to log EstimateGas success: %v\n", logErr)
	}

	return estimatedGas, nil
}

// GasPrice implements the Service interface - gets gas price from routing service
func (s *ServiceImpl) GasPrice(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get fee statistics directly from routing service
	feeStats, err := block.GetFeeStatisticsFromRouting()
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("GasPrice failed to get fee statistics: %v", err), "GasPrice", -1); logErr != nil {
			fmt.Printf("Failed to log GasPrice error: %v\n", logErr)
		}
		// Return fallback value on error (use 35 gwei minimum)
		return big.NewInt(35000000000), nil
	}

	// Get standard recommended fee (wei)
	gasPrice := big.NewInt(int64(feeStats.RecommendedFees.Standard))

	// Enforce minimum gas price:
	// - if zero or less than 20 gwei, use 35 gwei
	twentyGwei := big.NewInt(20000000000)
	thirtyFiveGwei := big.NewInt(35000000000)
	if gasPrice.Sign() <= 0 || gasPrice.Cmp(twentyGwei) < 0 {
		gasPrice = new(big.Int).Set(thirtyFiveGwei)
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("GasPrice returned to client: %s", gasPrice.String()), "GasPrice", 1); logErr != nil {
		fmt.Printf("Failed to log GasPrice success: %v\n", logErr)
	}

	return gasPrice, nil
}

// GetCode implements the Service interface - retrieves contract code at a specific address and block
func (s *ServiceImpl) GetCode(ctx context.Context, addr string, block *big.Int) (string, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Log the operation
	if err := Logger.LogData(opCtx, fmt.Sprintf("GetCode called for address: %s, block: %s", addr, block.String()), "GetCode", 1); err != nil {
		fmt.Printf("Failed to log GetCode operation: %v\n", err)
	}

	// For now, return "0x" as there's no contract code storage implemented yet
	// TODO: Implement actual contract code retrieval from state/storage
	// This would typically involve:
	// 1. Getting the state at the specified block
	// 2. Looking up the account at the given address
	// 3. Returning the code field (empty for EOAs, bytecode for contracts)

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("GetCode returned 0x for address: %s", addr), "GetCode", 1); logErr != nil {
		fmt.Printf("Failed to log GetCode success: %v\n", logErr)
	}

	return "0x", nil
}

// FeeHistory implements the Service interface - retrieves fee history for the last N blocks
func (s *ServiceImpl) FeeHistory(ctx context.Context, blockCount uint64, newest *big.Int, perc []float64) (map[string]any, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Determine the newest block number
	var newestNum *big.Int
	if newest != nil {
		newestNum = newest
	} else {
		// Get latest block if newest not specified
		latest, err := s.BlockNumber(ctx)
		if err != nil {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("FeeHistory failed to get latest block: %v", err), "FeeHistory", -1); logErr != nil {
				fmt.Printf("Failed to log FeeHistory error: %v\n", logErr)
			}
			return nil, err
		}
		newestNum = latest
	}

	// Calculate oldest block number
	// We need blockCount + 1 blocks (newest block + blockCount blocks before it)
	oldestNum := new(big.Int).Sub(newestNum, big.NewInt(int64(blockCount)))
	if oldestNum.Sign() < 0 {
		oldestNum = big.NewInt(0)
	}

	// Initialize result arrays
	baseFeePerGas := make([]string, 0, blockCount+1)
	gasUsedRatio := make([]float64, 0, blockCount+1)
	var rewards [][]string

	// If percentiles are provided, initialize rewards array
	if len(perc) > 0 {
		rewards = make([][]string, 0, blockCount+1)
	}

	// Fetch blocks from newest to oldest (inclusive)
	current := new(big.Int).Set(newestNum)
	blocksToFetch := blockCount + 1

	for i := uint64(0); i < blocksToFetch && current.Sign() >= 0 && current.Cmp(oldestNum) >= 0; i++ {
		block, err := s.BlockByNumber(ctx, current, false)
		if err != nil {
			// If block doesn't exist, skip it
			current.Sub(current, big.NewInt(1))
			continue
		}

		// Extract base fee per gas
		var baseFeeHex string
		if len(block.Header.BaseFee) > 0 {
			baseFeeBig := new(big.Int).SetBytes(block.Header.BaseFee)
			baseFeeHex = "0x" + baseFeeBig.Text(16)
		} else {
			// If no base fee (pre-EIP-1559), set to 0x0
			baseFeeHex = "0x0"
		}
		baseFeePerGas = append(baseFeePerGas, baseFeeHex)

		// Calculate gas used ratio
		var gasUsedRatioVal float64
		if block.Header.GasLimit > 0 {
			gasUsedRatioVal = float64(block.Header.GasUsed) / float64(block.Header.GasLimit)
		} else {
			gasUsedRatioVal = 0.0
		}
		gasUsedRatio = append(gasUsedRatio, gasUsedRatioVal)

		// Calculate rewards if percentiles are provided
		if len(perc) > 0 {
			blockRewards := make([]string, len(perc))
			// TODO: Calculate actual rewards from transaction priority fees
			// For now, set all rewards to 0x0
			for j := range perc {
				blockRewards[j] = "0x0"
			}
			rewards = append(rewards, blockRewards)
		}

		// Move to previous block
		current.Sub(current, big.NewInt(1))
	}

	// Build result map
	result := map[string]any{
		"oldestBlock": fmt.Sprintf("0x%x", oldestNum.Uint64()),
		// "baseFeePerGas": baseFeePerGas,
		"gasUsedRatio": gasUsedRatio,
	}

	// Add rewards if provided
	if len(perc) > 0 && len(rewards) > 0 {
		result["reward"] = rewards
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("FeeHistory returned for blockCount: %d, newest: %s", blockCount, newestNum.String()), "FeeHistory", 1); logErr != nil {
		fmt.Printf("Failed to log FeeHistory success: %v\n", logErr)
	}

	return result, nil
}
