package Service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	block "gossipnode/Block"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	Utils "gossipnode/gETH/Facade/Service/utils"
	"math/big"
	"strings"
	"time"

	scTracer "gossipnode/SmartContract/pkg/tracer"
	"gossipnode/SmartContract/pkg/client"
	smartcontractpb "gossipnode/SmartContract/proto"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// ServiceImpl implements the Service interface
type ServiceImpl struct {
	ChainIDValue      int
	SmartContractPort int
	scClient          *client.Client
}

// NewService creates a new service implementation
func NewService(chainID int, smartRPC int) Service {
	scClient, err := client.NewClient(fmt.Sprintf("localhost:%d", smartRPC))
	if err != nil {
		logger().Error(context.Background(), "Failed to connect to SmartContract gRPC server", err)
	}
	return &ServiceImpl{
		ChainIDValue:      chainID,
		SmartContractPort: smartRPC,
		scClient:          scClient,
	}
}

func (s *ServiceImpl) ChainID(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Log the operation
	if err := Logger.LogData(opCtx, "ChainID returned to the client", "ChainID", 1); err != nil {
		// Log error but don't fail the operation
		logger().Error(opCtx, "Failed to log ChainID operation", err)
	}

	return big.NewInt(int64(s.ChainIDValue)), nil
}

func (s *ServiceImpl) CompileSolidity(ctx context.Context, source string, optimize bool, runs uint32) (*SolcCompileResult, error) {
	resp, err := s.scClient.CompileContract(ctx, &smartcontractpb.CompileRequest{
		SourceCode:   source,
		Optimize:     optimize,
		OptimizeRuns: runs,
	})
	if err != nil {
		return nil, err
	}

	// If the compiler returned errors in the contract object, return them
	if resp.Contract != nil && len(resp.Contract.Errors) > 0 {
		return &SolcCompileResult{
			Errors: resp.Contract.Errors,
		}, nil
	}

	// Check if top-level error exists
	if resp.Error != "" {
		return &SolcCompileResult{
			Errors: []string{resp.Error},
		}, nil
	}

	if resp.Contract == nil {
		return nil, fmt.Errorf("compilation failed: no contract produced")
	}

	return &SolcCompileResult{
		ABI:              resp.Contract.Abi,
		Bytecode:         resp.Contract.Bytecode,
		DeployedBytecode: resp.Contract.DeployedBytecode,
		Errors:           resp.Contract.Errors,
		// Warnings would be added if available in proto
	}, nil
}

func (s *ServiceImpl) ClientVersion(ctx context.Context) (string, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ClientVersion := "JMDT/v1.0.0"

	// Log the operation
	if err := Logger.LogData(opCtx, "ClientVersion returned to the client", "ClientVersion", 1); err != nil {
		// Log error but don't fail the operation
		logger().Error(opCtx, "Failed to log ClientVersion operation", err)
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
			logger().Error(opCtx, "Failed to log BlockNumber error", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockNumber returned to the client: %d", BlockNumber), "BlockNumber", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log BlockNumber success", logErr)
	}

	return big.NewInt(int64(BlockNumber)), nil
}

func (s *ServiceImpl) GetTransactionCount(ctx context.Context, addr string, block string) (*big.Int, error) {
	// Create a new context with timeout for this operation
	_, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Return the transaction count for the given address of latest block
	Transactions, err := DB_OPs.CountTransactions(nil)
	if err != nil {
		return nil, err
	}

	// fmt.Println("Transactions: ", Transactions)
	// Convert the Transactions to big.Int
	TransactionsBigInt := big.NewInt(int64(Transactions))

	return TransactionsBigInt, nil
}

func (s *ServiceImpl) BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Types.Block, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ZKBlock, err := DB_OPs.GetZKBlockByNumber(nil, num.Uint64())
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber failed: %v", err), "BlockByNumber", -1); logErr != nil {
	logger().Error(opCtx, "Failed to log BlockByNumber error", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber returned to the client: %d", ZKBlock.BlockNumber), "BlockByNumber", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log BlockByNumber success", logErr)
	}

	// Convert the ZKBlock from GetZKBlockByNumber to Block
	block := Utils.ConvertZKBlockToBlock(ZKBlock)
	if block == nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber failed: %v", err), "BlockByNumber", -1); logErr != nil {
	logger().Error(opCtx, "Failed to log BlockByNumber error", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("BlockByNumber returned to the client: %d", ZKBlock.BlockNumber), "BlockByNumber", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log BlockByNumber success", logErr)
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
	logger().Debug(opCtx, "Address conversion", ion.String("original", addr), ion.String("converted", convertedAddr.Hex()))
	AccountDetails, err := DB_OPs.GetAccount(nil, convertedAddr)
	if err != nil {
	logger().Error(opCtx, "GetAccount error", err)
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
	logger().Error(opCtx, "Failed to log Balance account creation error", logErr)
				}
				return nil, err
			}

			// Log account creation
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance created new account for address: %s", addr), "Balance", 1); logErr != nil {
				logger().Error(opCtx, "Failed to log Balance account creation", logErr)
			}

			// Return zero balance for new account
			return big.NewInt(0), nil
		}

		// For other errors, log and return
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance failed: %v", err), "Balance", -1); logErr != nil {
	logger().Error(opCtx, "Failed to log Balance error", logErr)
		}
		return nil, err
	}

	// Debug: Print account details
	logger().Debug(opCtx, "Account found", ion.String("balance", AccountDetails.Balance), ion.String("address", AccountDetails.Address.Hex()), ion.String("did", AccountDetails.DIDAddress))

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance returned to the client: %s", AccountDetails.Balance), "Balance", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log Balance success", logErr)
	}

	// Convert the balance from string to big.Int
	balance, err := Utils.ConvertBalance(AccountDetails.Balance)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance failed: %v", err), "Balance", -1); logErr != nil {
	logger().Error(opCtx, "Failed to log Balance error", logErr)
		}
		return nil, err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance returned to the client: %s", AccountDetails.Balance), "Balance", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log Balance success", logErr)
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
	logger().Error(opCtx, "Failed to log SendRawTx hex decode error", logErr)
		}
		return "", fmt.Errorf("failed to decode hex string: %w", err)
	}

	// Try to parse as JSON first (for test compatibility)
	var tx config.Transaction
	err = json.Unmarshal(rawBytes, &tx)
	if err != nil {
		// If JSON parsing fails, try to parse as RLP-encoded transaction
	logger().Debug(opCtx, "JSON parsing failed, trying RLP parsing")

		// Parse RLP-encoded transaction
		var ethTx ethtypes.Transaction
		err = rlp.DecodeBytes(rawBytes, &ethTx)
		if err != nil {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx failed to parse RLP transaction: %v", err), "SendRawTx", -1); logErr != nil {
	logger().Error(opCtx, "Failed to log SendRawTx RLP parse error", logErr)
			}
			return "", fmt.Errorf("failed to parse RLP transaction: %w", err)
		}

		// Convert Ethereum transaction to our config.Transaction format
		tx = convertEthTxToConfigTx(&ethTx)
	logger().Debug(opCtx, "Converted RLP transaction")
	} else {
	logger().Debug(opCtx, "JSON transaction parsed")
	}

	hash, err := block.SubmitRawTransaction(context.Background(), &tx)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx failed: %v", err), "SendRawTx", -1); logErr != nil {
	logger().Error(opCtx, "Failed to log SendRawTx error", logErr)
		}
		// Debugging
	logger().Error(opCtx, "SubmitRawTransaction failed", err)
		return "", err
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("SendRawTx returned to the client: %s", hash), "SendRawTx", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log SendRawTx success", logErr)
	}
	// Debugging
	logger().Info(opCtx, "SubmitRawTransaction success", ion.String("hash", hash))

	return hash, nil
}

// convertEthTxToConfigTx converts an Ethereum transaction to our config.Transaction format
func convertEthTxToConfigTx(ethTx *ethtypes.Transaction) config.Transaction {
	// Get the sender address
	from, _ := ethtypes.Sender(ethtypes.NewEIP155Signer(ethTx.ChainId()), ethTx)

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
	if ethTx.Type() == ethtypes.LegacyTxType {
		tx.GasPrice = ethTx.GasPrice()
	} else if ethTx.Type() == ethtypes.AccessListTxType {
		tx.GasPrice = ethTx.GasPrice()
	} else if ethTx.Type() == ethtypes.DynamicFeeTxType {
		tx.MaxFee = ethTx.GasFeeCap()
		tx.MaxPriorityFee = ethTx.GasTipCap()
	}

	// Set signature components
	v, r, s := ethTx.RawSignatureValues()
	tx.V = v
	tx.R = r
	tx.S = s

	// Debugging
	logger().Debug(opCtx, "Transaction details", ion.String("hash", tx.Hash.Hex()))
	logger().Debug(opCtx, "Transaction sender", ion.String("from", tx.From.Hex()))
	logger().Debug(opCtx, "Transaction recipient", ion.String("to", tx.To.Hex()))
	logger().Debug(opCtx, "Transaction value", ion.String("value", tx.Value.String()))
	logger().Debug(opCtx, "Transaction type", ion.Int("type", int(tx.Type)))
	logger().Debug(opCtx, "Transaction timestamp", ion.Int("timestamp", int(tx.Timestamp)))
	logger().Debug(opCtx, "Chain ID", ion.String("chain_id", tx.ChainID.String()))
	logger().Debug(opCtx, "Transaction nonce", ion.Int("nonce", int(tx.Nonce)))
	logger().Debug(opCtx, "Gas limit", ion.Int("gas_limit", int(tx.GasLimit)))
	logger().Debug(opCtx, "Gas price", ion.String("gas_price", tx.GasPrice.String()))
	logger().Debug(opCtx, "Max fee", ion.String("max_fee", tx.MaxFee.String()))
	logger().Debug(opCtx, "Max priority fee", ion.String("max_priority_fee", tx.MaxPriorityFee.String()))
	logger().Debug(opCtx, "Transaction data length", ion.Int("data_len", len(tx.Data)))
	logger().Debug(opCtx, "Access list present")
	logger().Debug(opCtx, "Transaction V", ion.String("v", tx.V.String()))
	logger().Debug(opCtx, "Transaction R", ion.String("r", tx.R.String()))
	logger().Debug(opCtx, "Transaction S", ion.String("s", tx.S.String()))

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
	logger().Error(opCtx, "Failed to log TxByHash error", logErr)
		}
		return nil, err
	}

	// Get the transaction
	ZKTx, err := DB_OPs.GetTransactionByHash(nil, normalizedHash)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash failed: %v", err), "TxByHash", -1); logErr != nil {
	logger().Error(opCtx, "Failed to log TxByHash error", logErr)
		}
		return nil, err
	}

	// Convert the ZKTx from GetTransactionByHash to Tx
	tx := Utils.ConvertTrabsactionToTx(ZKTx)
	if tx == nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("TxByHash failed: %v", err), "TxByHash", -1); logErr != nil {
	logger().Error(opCtx, "Failed to log TxByHash error", logErr)
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
		logger().Error(opCtx, "Failed to log TxByHash success", logErr)
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
	logger().Error(opCtx, "Failed to log ReceiptByHash error", logErr)
			}
			// Return error that will be formatted as JSON-RPC error with code -32000
			return nil, fmt.Errorf("transaction not found")
		}
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash failed: %v", err), "ReceiptByHash", -1); logErr != nil {
	logger().Error(opCtx, "Failed to log ReceiptByHash error", logErr)
		}
		return nil, err
	}

	// If receipt is nil and no error, it means tx_processing was -1
	// Return nil to indicate result should be null in JSON-RPC response
	if receipt == nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash: tx_processing=-1 for %s, returning null", hash), "ReceiptByHash", 1); logErr != nil {
			logger().Error(opCtx, "Failed to log ReceiptByHash", logErr)
		}
		return nil, nil
	}

	// Get the transaction to extract from and to addresses
	tx, txErr := DB_OPs.GetTransactionByHash(nil, hash)
	if txErr != nil {
		// Log but don't fail - we can still return receipt without from/to
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash: failed to get transaction for from/to: %v", txErr), "ReceiptByHash", -1); logErr != nil {
			logger().Error(opCtx, "Failed to log", logErr)
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
		logger().Error(opCtx, "Failed to log ReceiptByHash success", logErr)
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
	logger().Error(opCtx, "Failed to log GetLogs error", logErr)
		}
		return nil, err
	}

	return logs, nil
}

// Call implements the Service interface - calls smart contract via gRPC
func (s *ServiceImpl) Call(ctx context.Context, msg Types.CallMsg, block *big.Int) ([]byte, error) {
	if s.scClient == nil {
		return nil, fmt.Errorf("SmartContract client not initialized")
	}

	caller := common.FromHex(msg.From)
	contractAddr := common.FromHex(msg.To)

	resp, err := s.scClient.CallContract(ctx, caller, contractAddr, msg.Data)
	if err != nil {
		return nil, fmt.Errorf("smart contract call failed: %v", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("smart contract execution error: %s", resp.Error)
	}

	return common.FromHex(resp.ReturnData), nil
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
		logger().Error(opCtx, "Failed to log EstimateGas success", logErr)
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
	logger().Error(opCtx, "Failed to log GasPrice error", logErr)
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
		logger().Error(opCtx, "Failed to log GasPrice success", logErr)
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
		logger().Error(opCtx, "Failed to log GetCode operation", err)
	}

	if s.scClient == nil {
		return "0x", fmt.Errorf("SmartContract client not initialized")
	}

	contractAddr := common.FromHex(addr)
	resp, err := s.scClient.GetContractCode(opCtx, contractAddr)
	if err != nil {
		// Just return 0x for now if it fails
		return "0x", nil
	}

	// Log success
	if logErr := Logger.LogData(opCtx, fmt.Sprintf("GetCode returned for address: %s", addr), "GetCode", 1); logErr != nil {
		logger().Error(opCtx, "Failed to log GetCode success", logErr)
	}

	if resp.Code == "" {
		return "0x", nil
	}

	return resp.Code, nil
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
	logger().Error(opCtx, "Failed to log FeeHistory error", logErr)
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
		logger().Error(opCtx, "Failed to log FeeHistory success", logErr)
	}

	return result, nil
}

func (s *ServiceImpl) GetStorageAt(ctx context.Context, address string, slot string, blockNum string) (string, error) {
	if s.scClient == nil {
		return "0x0000000000000000000000000000000000000000000000000000000000000000", nil
	}
	resp, err := s.scClient.GetStorage(ctx, common.HexToAddress(address).Bytes(), common.HexToHash(slot).Bytes())
	if err != nil {
		return "0x0000000000000000000000000000000000000000000000000000000000000000", nil
	}
	return resp.Value, nil
}

func (s *ServiceImpl) GetGasPrice(ctx context.Context) (string, error) {
	return hexutil.EncodeBig(config.DefaultGasPrice), nil
}

func (s *ServiceImpl) GetFeeHistory(ctx context.Context, blockCount int, newestBlock string, rewardPercentiles []float64) (interface{}, error) {
	history, err := s.FeeHistory(ctx, uint64(blockCount), nil, rewardPercentiles)
	if err != nil || len(history) == 0 {
		return map[string]interface{}{
			"oldestBlock": "0x0",
			"baseFeePerGas": []string{hexutil.EncodeBig(config.DefaultGasPrice)},
			"gasUsedRatio": []float64{0.0},
			"reward": [][]string{},
		}, nil
	}
	return history, nil
}

func (s *ServiceImpl) GetMaxPriorityFeePerGas(ctx context.Context) (string, error) {
	return hexutil.EncodeBig(config.DefaultPriorityFeePerGas), nil
}

func (s *ServiceImpl) IsListening(ctx context.Context) (bool, error) {
	return true, nil
}

func (s *ServiceImpl) GetPeerCount(ctx context.Context) (string, error) {
	return "0x1", nil
}

// TraceTransaction implements debug_traceTransaction.
//
// KNOWN LIMITATION (Phase 5): This implementation re-executes the call
// against the CURRENT StateDB, not a historical snapshot of the pre-execution
// state.  For read-only / view calls the gas usage and return value are
// accurate.  For state-mutating calls the opcode trace may differ from the
// original execution if storage has changed since the transaction landed.
//
// Full historical tracing (fetching the Pebble snapshot at the parent block's
// stateRoot) is deferred to Phase 5.  Until then, Foundry users should pass
// --no-storage-caching to forge script/test when replay accuracy is required.
func (s *ServiceImpl) TraceTransaction(ctx context.Context, txHash string) (json.RawMessage, error) {
	_, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Normalise hash
	if !strings.HasPrefix(strings.ToLower(txHash), "0x") {
		txHash = "0x" + txHash
	}

	// Fetch the original transaction from ImmuDB
	zkTx, err := DB_OPs.GetTransactionByHash(nil, txHash)
	if err != nil {
		return nil, fmt.Errorf("TraceTransaction: tx not found: %w", err)
	}
	if zkTx == nil {
		return nil, fmt.Errorf("TraceTransaction: tx not found")
	}

	// Derive call parameters
	var from common.Address
	if zkTx.From != nil {
		from = *zkTx.From
	}

	var to *common.Address
	if zkTx.To != nil {
		addr := *zkTx.To
		to = &addr
	}

	value := zkTx.Value
	if value == nil {
		value = new(big.Int)
	}

	gasLimit := zkTx.GasLimit
	if gasLimit == 0 {
		gasLimit = 3_000_000 // sensible default
	}

	// Initialise a best-effort current StateDB
	// NOTE: This uses the live state, not the historical pre-tx snapshot.
	traceResult, err := scTracer.TraceTransaction(
		from,
		to,
		zkTx.Data,
		value,
		gasLimit,
		s.ChainIDValue,
	)
	if err != nil {
		return nil, err
	}

	return traceResult, nil
}
