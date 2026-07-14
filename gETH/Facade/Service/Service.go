package Service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	block "gossipnode/Block"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/version"
	"gossipnode/gETH/Facade/Service/Types"
	Utils "gossipnode/gETH/Facade/Service/utils"

	"github.com/ethereum/go-ethereum/core/types"
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

func (s *ServiceImpl) GetChainIDValue() *big.Int {
	return big.NewInt(int64(s.ChainIDValue))
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

	clientVersion := version.ClientVersion()

	// Log the operation
	if err := Logger.LogData(opCtx, "ClientVersion returned to the client", "ClientVersion", 1); err != nil {
		// Log error but don't fail the operation
		fmt.Printf("Failed to log ClientVersion operation: %v\n", err)
	}

	return clientVersion, nil
}

func (s *ServiceImpl) BlockNumber(ctx context.Context) (*big.Int, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Pass the context to the database operation
	BlockNumber, err := DB_OPs.GetLatestBlockNumber(opCtx, nil)
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
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Normalize the address (handles mixed-case / checksum addresses)
	convertedAddr := Utils.ConvertAddressCaseInsensitive(addr)

	// TxNonce on the Account record is the authoritative Ethereum nonce.
	// It is maintained by block processing as: account.TxNonce = tx.Nonce + 1
	// CountTransactionsByAccount is NOT used here because it counts both FROM and TO
	// transactions, which would inflate the nonce for recipient addresses.
	account, err := DB_OPs.GetAccount(nil, convertedAddr)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
			// Address has no transactions yet — nonce is 0
			return big.NewInt(0), nil
		}
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("GetTransactionCount failed: %v", err), "GetTransactionCount", -1); logErr != nil {
			fmt.Printf("Failed to log GetTransactionCount error: %v\n", logErr)
		}
		return nil, err
	}

	if logErr := Logger.LogData(opCtx, fmt.Sprintf("GetTransactionCount returned nonce %d for %s", account.TxNonce, addr), "GetTransactionCount", 1); logErr != nil {
		fmt.Printf("Failed to log GetTransactionCount: %v\n", logErr)
	}

	return new(big.Int).SetUint64(account.TxNonce), nil
}

func (s *ServiceImpl) BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Types.Block, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ZKBlock, err := DB_OPs.ReadZKBlockByNumber(opCtx, nil, num.Uint64())
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

// LatestL1CommitBlock returns the most recent block that has L1 commit data.
//
// Fast path (O(1)): Block/Server.go maintains an atomic cache of the latest
// committed block number, updated on every /api/l1-commit* call. If the cache
// is warm we do a single point-lookup.
//
// Cold path (first call after restart before any commit arrives): scans
// backwards up to 10 000 blocks — exits immediately on the first hit.
func (s *ServiceImpl) LatestL1CommitBlock(ctx context.Context) (*Types.Block, error) {
	const maxScan = 10_000

	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Fast path — cache is warm.
	if cached := block.GetLatestL1CommitBlockNum(); cached > 0 {
		zkBlock, err := DB_OPs.ReadZKBlockByNumber(opCtx, nil, cached)
		if err == nil && zkBlock != nil && zkBlock.L1TxHash != "" {
			return Utils.ConvertZKBlockToBlock(zkBlock), nil
		}
		// Cache pointed at a block without L1 data (shouldn't happen, but fall through).
	}

	// Cold path — scan backwards from chain tip.
	latest, err := DB_OPs.GetLatestBlockNumber(opCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("LatestL1CommitBlock: get latest block: %w", err)
	}

	start := int64(latest)
	end := start - maxScan
	if end < 0 {
		end = 0
	}

	for n := start; n >= end; n-- {
		zkBlock, err := DB_OPs.ReadZKBlockByNumber(opCtx, nil, uint64(n))
		if err != nil || zkBlock == nil {
			continue
		}
		if zkBlock.L1TxHash != "" {
			return Utils.ConvertZKBlockToBlock(zkBlock), nil
		}
	}

	return nil, nil // no committed block found in window
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
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
			// Auto-create and propagate the account
			didAddress := fmt.Sprintf("%s%s:%s", DB_OPs.DIDPrefix, "jmdn", convertedAddr.Hex())
			doc := Utils.DIDDoc{
				Address:    convertedAddr,
				DIDAddress: didAddress,
				Metadata:   nil,
			}
			if createErr := Utils.CreateAccountandPropagateDID(doc); createErr != nil {
				if logErr := Logger.LogData(opCtx, fmt.Sprintf("Failed to auto-create and propagate DID %s: %v", convertedAddr.Hex(), createErr), "Balance", -1); logErr != nil {
					fmt.Printf("Failed to log Balance error: %v\n", logErr)
				}
			} else {
				if logErr := Logger.LogData(opCtx, fmt.Sprintf("Auto-created and propagated DID %s via eth_getBalance", convertedAddr.Hex()), "Balance", 1); logErr != nil {
					fmt.Printf("Failed to log Balance success: %v\n", logErr)
				}
			}

			// Log and return zero balance without writing to database
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance returned zero for non-existent address: %s", addr), "Balance", 1); logErr != nil {
				fmt.Printf("Failed to log Balance success: %v\n", logErr)
			}
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

		// Parse transaction — UnmarshalBinary handles all types:
		// legacy (no prefix), EIP-2930 (0x01), EIP-1559 (0x02).
		// rlp.DecodeBytes cannot handle typed transactions (0x01/0x02 prefix).
		var ethTx types.Transaction
		err = ethTx.UnmarshalBinary(rawBytes)
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

	hash, err := block.SubmitRawTransaction(opCtx, &tx)
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
	from, _ := types.Sender(types.LatestSignerForChainID(ethTx.ChainId()), ethTx)

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
	if tx.To != nil {
		fmt.Println("To: ", tx.To.Hex())
	} else {
		fmt.Println("To: nil (contract creation)")
	}
	if tx.Value != nil {
		fmt.Println("Value: ", tx.Value.String())
	}
	fmt.Println("Type: ", tx.Type)
	fmt.Println("Timestamp: ", tx.Timestamp)
	if tx.ChainID != nil {
		fmt.Println("ChainID: ", tx.ChainID.String())
	}
	fmt.Println("Nonce: ", tx.Nonce)
	fmt.Println("GasLimit: ", tx.GasLimit)
	if tx.GasPrice != nil {
		fmt.Println("GasPrice: ", tx.GasPrice.String())
	}
	if tx.MaxFee != nil {
		fmt.Println("MaxFee: ", tx.MaxFee.String())
	}
	if tx.MaxPriorityFee != nil {
		fmt.Println("MaxPriorityFee: ", tx.MaxPriorityFee.String())
	}
	fmt.Println("Data: ", tx.Data)
	fmt.Println("AccessList: ", tx.AccessList)
	if tx.V != nil {
		fmt.Println("V: ", tx.V.String())
	}
	if tx.R != nil {
		fmt.Println("R: ", tx.R.String())
	}
	if tx.S != nil {
		fmt.Println("S: ", tx.S.String())
	}

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
	block, err := DB_OPs.GetTransactionBlock(opCtx, nil, normalizedHash)
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
		// Per EIP-1474: eth_getTransactionReceipt MUST return result:null (not a JSON-RPC error)
		// when the tx is not yet mined. An error response causes MetaMask to stop polling
		// and permanently show the tx as "submitted".
		if err.Error() == "transaction not found" {
			if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash: tx not yet mined (returning null): %s", hash), "ReceiptByHash", -1); logErr != nil {
				fmt.Printf("Failed to log ReceiptByHash: %v\n", logErr)
			}
			return nil, nil
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

	// Add effectiveGasPrice from transaction.
	// EIP-1559: effectiveGasPrice = min(maxFeePerGas, baseFee + maxPriorityFeePerGas)
	// baseFee is the network constant (35 gwei) used across all RPC responses.
	{
		var effectiveGasPrice *big.Int
		if tx != nil && tx.GasPrice != nil {
			// Legacy (type 0) and EIP-2930 (type 1)
			effectiveGasPrice = tx.GasPrice
		} else if tx != nil && tx.MaxFee != nil {
			tip := tx.MaxPriorityFee
			if tip == nil {
				tip = big.NewInt(0)
			}
			basePlusTip := new(big.Int).Add(big.NewInt(config.BaseFeeWei), tip)
			if tx.MaxFee.Cmp(basePlusTip) < 0 {
				effectiveGasPrice = new(big.Int).Set(tx.MaxFee)
			} else {
				effectiveGasPrice = basePlusTip
			}
		} else {
			// Fallback: always emit effectiveGasPrice (EIP-1559 requires it)
			effectiveGasPrice = big.NewInt(config.BaseFeeWei)
		}
		receiptMap["effectiveGasPrice"] = "0x" + effectiveGasPrice.Text(16)
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
	feeStats, err := block.GetFeeStatisticsFromRouting(opCtx)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("GasPrice failed to get fee statistics: %v", err), "GasPrice", -1); logErr != nil {
			fmt.Printf("Failed to log GasPrice error: %v\n", logErr)
		}
		// Return fallback value on error (use 35 gwei minimum)
		return big.NewInt(config.BaseFeeWei), nil
	}

	// Get standard recommended fee (wei)
	gasPrice := big.NewInt(int64(feeStats.RecommendedFees.Standard))

	// Enforce minimum gas price: use BaseFeeWei (35 gwei) as the floor.
	twentyGwei := big.NewInt(20_000_000_000)
	if gasPrice.Sign() <= 0 || gasPrice.Cmp(twentyGwei) < 0 {
		gasPrice = big.NewInt(config.BaseFeeWei)
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

	// JMDN does not implement variable base fees — return the same 35 gwei constant
	// used by eth_getBlockByNumber. No DB reads needed.
	baseFeeConstant := "0x" + big.NewInt(config.BaseFeeWei).Text(16)

	count := newestNum.Uint64() - oldestNum.Uint64() + 1
	baseFeePerGas := make([]string, count)
	gasUsedRatio := make([]float64, count)
	for i := range baseFeePerGas {
		baseFeePerGas[i] = baseFeeConstant
		gasUsedRatio[i] = 0.5 // neutral placeholder
	}

	var rewards [][]string
	if len(perc) > 0 {
		rewards = make([][]string, count)
		for i := range rewards {
			row := make([]string, len(perc))
			for j := range row {
				row[j] = "0x0"
			}
			rewards[i] = row
		}
	}

	// Build result map
	result := map[string]any{
		"oldestBlock":   fmt.Sprintf("0x%x", oldestNum.Uint64()),
		"baseFeePerGas": baseFeePerGas,
		"gasUsedRatio":  gasUsedRatio,
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

// TxPoolContent returns all pending transactions from MRE in the standard
// txpool_content format: {"pending": {from: {nonce: <txobj>}}, "queued": {}}.
// Uses PeekPendingTransactions (non-destructive) via the MRE v1 gRPC service.
func (s *ServiceImpl) TxPoolContent(ctx context.Context) (map[string]any, error) {
	routingClient, err := block.GetRoutingClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("txpool_content: routing client unavailable: %w", err)
	}

	// Cap at 5000. MRE rejects limit <= 0, and pre-allocates make([]*Tx, 0, limit),
	// so MaxInt32 would be a memory bomb. True "unbounded" semantics would need an
	// MRE-side change to treat limit=0 as capped-prealloc; use this constant until then.
	batch, err := routingClient.PeekPendingTransactions(ctx, 5000)
	if err != nil {
		return nil, fmt.Errorf("txpool_content: %w", err)
	}

	// Group by sender → nonce → tx object (standard txpool_content shape).
	pending := make(map[string]map[string]any)
	for _, tx := range batch.GetTransactions() {
		from := strings.ToLower(tx.GetFrom())
		if from == "" {
			continue
		}
		if pending[from] == nil {
			pending[from] = make(map[string]any)
		}
		nonce := fmt.Sprintf("%d", tx.GetNonce())
		pending[from][nonce] = mempoolTxToRPCObject(tx)
	}

	return map[string]any{
		"pending": pending,
		"queued":  map[string]any{}, // JMDN mempool has no nonce-gap staging
	}, nil
}

// mempoolTxToRPCObject converts a proto Transaction to the Ethereum JSON-RPC tx object shape.
func mempoolTxToRPCObject(tx interface {
	GetHash() string
	GetFrom() string
	GetTo() string
	GetValue() string
	GetNonce() uint64
	GetGasLimit() string
	GetGasPrice() string
	GetMaxFee() string
	GetMaxPriorityFee() string
	GetData() []byte
	GetType() uint32
	GetV() string
	GetR() string
	GetS() string
}) map[string]any {
	decToHex := func(dec string) string {
		if dec == "" {
			return "0x0"
		}
		n, ok := new(big.Int).SetString(dec, 10)
		if !ok || n == nil {
			return "0x0"
		}
		return "0x" + n.Text(16)
	}

	// Contract creations have an empty "to" field; geth renders those as null.
	var to any
	if t := tx.GetTo(); t != "" {
		to = strings.ToLower(t)
	}

	obj := map[string]any{
		"blockHash":        nil,
		"blockNumber":      nil,
		"transactionIndex": nil,
		"hash":             tx.GetHash(),
		"from":             strings.ToLower(tx.GetFrom()),
		"to":               to,
		"nonce":            fmt.Sprintf("0x%x", tx.GetNonce()),
		"gas":              decToHex(tx.GetGasLimit()),
		"value":            decToHex(tx.GetValue()),
		"input":            "0x" + hex.EncodeToString(tx.GetData()),
		"type":             fmt.Sprintf("0x%x", tx.GetType()),
		"v":                tx.GetV(), // already "0x…" hex — see getSignatureString, gRPCclient.go:756
		"r":                tx.GetR(),
		"s":                tx.GetS(),
	}

	if tx.GetType() == 2 {
		obj["maxFeePerGas"] = decToHex(tx.GetMaxFee())
		obj["maxPriorityFeePerGas"] = decToHex(tx.GetMaxPriorityFee())
	} else {
		obj["gasPrice"] = decToHex(tx.GetGasPrice())
	}

	return obj
}
