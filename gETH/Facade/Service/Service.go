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

	// Return the transaction count for the given address of latest block
	address := Utils.ConvertAddress(addr)
	Transactions, err := DB_OPs.GetTransactionsByAccount(nil, &address)
	if err != nil {
		return nil, err
	}

	fmt.Println("Transactions: ", Transactions)

	return big.NewInt(int64(len(Transactions))), nil
}

func (s *ServiceImpl) BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Types.Block, error) {
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
			didAddress := fmt.Sprintf("did:%s:%s", network, address.Hex())

			// Save the new account to database
			if createErr := DB_OPs.CreateAccount(nil, didAddress, address, nil); createErr != nil {
				if logErr := Logger.LogData(opCtx, fmt.Sprintf("Balance failed to create account: %v", createErr), "Balance", -1); logErr != nil {
					fmt.Printf("Failed to log Balance account creation error: %v\n", logErr)
				}
				return nil, createErr
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
	fmt.Println(">>>>>> SendRawTx received: ", rawHex)
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
		Timestamp: uint64(time.Now().Unix()),
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

func (s *ServiceImpl) ReceiptByHash(ctx context.Context, hash string) (map[string]any, error) {
	// Create a new context with timeout for this operation
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get the receipt from the database
	receipt, err := DB_OPs.GetReceiptByHash(nil, hash)
	if err != nil {
		if logErr := Logger.LogData(opCtx, fmt.Sprintf("ReceiptByHash failed: %v", err), "ReceiptByHash", -1); logErr != nil {
			fmt.Printf("Failed to log ReceiptByHash error: %v\n", logErr)
		}
		return nil, err
	}

	// Convert the receipt to a map for JSON serialization
	receiptMap := map[string]any{
		"transactionHash":   receipt.TxHash.Hex(),
		"blockHash":         receipt.BlockHash.Hex(),
		"blockNumber":       fmt.Sprintf("%x", receipt.BlockNumber),
		"transactionIndex":  fmt.Sprintf("%x", receipt.TransactionIndex),
		"status":            fmt.Sprintf("%x", receipt.Status),
		"type":              fmt.Sprintf("%x", receipt.Type),
		"gasUsed":           fmt.Sprintf("%x", receipt.GasUsed),
		"cumulativeGasUsed": fmt.Sprintf("%x", receipt.CumulativeGasUsed),
		"logs":              Utils.ConvertLogsToMap(receipt.Logs),
		"logsBloom":         fmt.Sprintf("%x", receipt.LogsBloom),
	}

	// Add contract address if present
	if receipt.ContractAddress != nil {
		receiptMap["contractAddress"] = receipt.ContractAddress.Hex()
	}

	// Add ZK-specific fields
	if len(receipt.ZKProof) > 0 {
		receiptMap["zkProof"] = fmt.Sprintf("%x", receipt.ZKProof)
	}
	if receipt.ZKStatus != "" {
		receiptMap["zkStatus"] = receipt.ZKStatus
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

// EstimateGas implements the Service interface - placeholder implementation
func (s *ServiceImpl) EstimateGas(ctx context.Context, msg Types.CallMsg) (uint64, error) {
	// TODO: Implement gas estimation functionality
	return 321000, nil // Return base gas cost as fallback
}

// GasPrice implements the Service interface - placeholder implementation
func (s *ServiceImpl) GasPrice(ctx context.Context) (*big.Int, error) {
	// TODO: Implement gas price calculation
	return big.NewInt(20000000000), nil // Return 20 gwei as fallback
}
