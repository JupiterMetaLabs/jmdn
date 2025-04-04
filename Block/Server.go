package Block

import (
	"fmt"
	"strconv"

	// "gossipnode/messaging"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/messaging"
	"log"
	"math/big"
	"net/http"
	"os"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/libp2p/go-libp2p/core/host"
)
type APIAccessTuple struct {
    Address     string   `json:"address"`
    StorageKeys []string `json:"storage_keys"`
}

// Request and response models
type TransactionRequest struct {
    From              string   `json:"from" binding:"required"`       // Add this line
    RecipientAddress  string   `json:"recipient_address" binding:"required"`
    Amount            string   `json:"amount" binding:"required"`     // String to preserve precision
    Nonce             uint64   `json:"nonce" binding:"required"`
    GasLimit          uint64   `json:"gas_limit" binding:"required"`
    GasPrice          string   `json:"gas_price" binding:"required"`  // String to preserve precision
    Data              string   `json:"data"`                          // Optional data
    MaxPriorityFee    string   `json:"max_priority_fee"`              // Optional for EIP-1559
    MaxFee            string   `json:"max_fee"`                       // Optional for EIP-1559
    ChainID           int64    `json:"chain_id" binding:"required"`
    AccessList        []APIAccessTuple `json:"access_list"`           // Optional
}

type TxnsType struct{
    TxnType string `json:"txn_type" binding:"required"`
    Txn TransactionRequest `json:"txn" binding:"required"`
}

type TransactionResponse struct {
    LegacyTx  *FullTxn `json:"legacy_tx,omitempty"`
    EIP1559Tx *FullTxn `json:"eip1559_tx,omitempty"`
}

type FullTxn struct {
    Transaction     *TransactionData `json:"transaction"`
    TransactionHash string           `json:"transaction_hash"`
}

type TransactionData struct {
    ChainID             string        `json:"chain_id"`
    From                string        `json:"from"` // Sender's address
    Nonce               uint64        `json:"nonce"`
    To                  string        `json:"to"`
    Value               string        `json:"value"`
    Data                string        `json:"data"`
    GasLimit            uint64        `json:"gas_limit"`
    GasPrice            string        `json:"gas_price,omitempty"`
    MaxPriorityFeePerGas string       `json:"max_priority_fee,omitempty"`
    MaxFeePerGas         string       `json:"max_fee,omitempty"`
    AccessList          []APIAccessTuple `json:"access_list,omitempty"`
    V                   string        `json:"v"`
    R                   string        `json:"r"`
    S                   string        `json:"s"`
    Type                string        `json:"type"`
}

// Global mutex to protect account access
var accountMutex sync.Mutex

// Convert API AccessTuple to Block.AccessList
func toBlockAccessList(apiList []APIAccessTuple) config.AccessList {
    if len(apiList) == 0 {
        return config.AccessList{}
    }
    
    result := make(config.AccessList, len(apiList))
    for i, tuple := range apiList {
        storageKeys := make([]common.Hash, len(tuple.StorageKeys))
        for j, key := range tuple.StorageKeys {
            storageKeys[j] = common.HexToHash(key)
        }
        
        result[i] = config.AccessTuple{
            Address:     common.HexToAddress(tuple.Address),
            StorageKeys: storageKeys,
        }
    }
    return result
}

// Convert Block.Transaction to TransactionData
func toTransactionData(tx *config.Transaction) *TransactionData {
    result := &TransactionData{
        ChainID:  tx.ChainID.String(),
        From:     tx.From.Hex(),
        Nonce:    tx.Nonce,
        Value:    tx.Value.String(),
        Data:     string(tx.Data),
        GasLimit: tx.GasLimit,
        V:        tx.V.String(),
        R:        tx.R.String(),
        S:        tx.S.String(),
    }
    
    // Set recipient address
    if tx.To != nil {
        result.To = tx.To.Hex()
    }
    
    // Set transaction type and type-specific fields
    if tx.MaxFeePerGas != nil {
        result.Type = "EIP-1559"
        result.MaxFeePerGas = tx.MaxFeePerGas.String()
        result.MaxPriorityFeePerGas = tx.MaxPriorityFeePerGas.String()
    } else {
        result.Type = "Legacy"
        if tx.GasPrice != nil {
            result.GasPrice = tx.GasPrice.String()
        }
    }
    
    // Set access list if present
    if tx.AccessList != nil && len(tx.AccessList) > 0 {
        result.AccessList = make([]APIAccessTuple, len(tx.AccessList))
        for i, tuple := range tx.AccessList {
            keys := make([]string, len(tuple.StorageKeys))
            for j, key := range tuple.StorageKeys {
                keys[j] = key.Hex()
            }
            
            result.AccessList[i] = APIAccessTuple{
                Address:     tuple.Address.Hex(),
                StorageKeys: keys,
            }
        }
    }
    
    return result
}

// Handler for generating transactions
func generateTransactions(c *gin.Context) {
    // var req TransactionRequest
	var req TxnsType
	var response TransactionResponse
    
    // Validate request
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }
    
    // Parse big integers from string
	amount, ok := new(big.Int).SetString(req.Txn.Amount, 10)    
	if !ok {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid amount"})
        return
    }
    
    gasPrice, ok := new(big.Int).SetString(req.Txn.GasPrice, 10)
    if !ok {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid gas price"})
        return
    }
    
    // Path to account file
    accountPath := "Account.json"
    chainID := big.NewInt(req.Txn.ChainID)
    
    // Convert data string to byte array
    data := []byte(req.Txn.Data)
    
    // Lock during signing to prevent race conditions
    accountMutex.Lock()
    defer accountMutex.Unlock()
    
	if req.TxnType == "legacy" {
    	// Generate Legacy Transaction
		legacyTx, err := GenerateLegacyTransaction(
			accountPath,
			chainID,
			req.Txn.RecipientAddress,
			amount,
			req.Txn.Nonce,
			req.Txn.GasLimit,
			gasPrice,
			data,
            req.Txn.From,
		)
		
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("legacy transaction error: %v", err)})
			return
		}
		
		// Calculate hash for legacy transaction
		legacyTxHash, err := Hash(legacyTx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("hash error: %v", err)})
			return
		}
		// Prepare response with legacy transaction
		response = TransactionResponse{
			LegacyTx: &FullTxn{
				Transaction:     toTransactionData(legacyTx),
				TransactionHash: legacyTxHash,
			},
		}
        // go func(){
        //     err := messaging.PropagateTransaction(globalHost, legacyTx, legacyTxHash)
        //     if err != nil {
        //         log.Printf("Error propagating transaction to network: %v", err)
        //     } else {
        //         log.Printf("Transaction %s successfully propagated to network", legacyTxHash)
        //     }
        // }()
         // Submit the transaction to the mempool
        go func(){
            err := SubmitToMempool(legacyTx, legacyTxHash)
            if err != nil {
                log.Printf("Error submitting transaction to mempool: %v", err)
            } else {
                log.Printf("Transaction %s successfully submitted to mempool", legacyTxHash)
            }
        }()
	}else{
		// Generate EIP-1559 transaction if maxFee and maxPriorityFee are provided
		if req.Txn.MaxFee != "" && req.Txn.MaxPriorityFee != "" {
			maxFee, ok := new(big.Int).SetString(req.Txn.MaxFee, 10)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid max fee"})
				return
			}
			
			maxPriorityFee, ok := new(big.Int).SetString(req.Txn.MaxPriorityFee, 10)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid max priority fee"})
				return
			}
			
			accessList := toBlockAccessList(req.Txn.AccessList)
			
			eip1559Tx, err := GenerateEIP1559Transaction(
				accountPath,
				chainID,
				req.Txn.RecipientAddress,
				amount,
				req.Txn.Nonce,
				req.Txn.GasLimit,
				maxFee,
				maxPriorityFee,
				data,
				accessList,
                req.Txn.From,
			)
			
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("EIP-1559 transaction error: %v", err)})
				return
			}
			
			// Calculate hash for EIP-1559 transaction
			eip1559TxHash, err := Hash(eip1559Tx)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("hash error: %v", err)})
				return
			}
			
			// Add EIP-1559 transaction to response
			response.EIP1559Tx = &FullTxn{
				Transaction:     toTransactionData(eip1559Tx),
				TransactionHash: eip1559TxHash,
			}

            // go func() {
            //     err := messaging.PropagateTransaction(globalHost, eip1559Tx, eip1559TxHash)
            //     if err != nil {
            //         log.Printf("Error propagating transaction to network: %v", err)
            //     } else {
            //         log.Printf("Transaction %s successfully propagated to network", eip1559TxHash)
            //     }
            // }()
            go func() {
                err := SubmitToMempool(eip1559Tx, eip1559TxHash)
                if err != nil {
                    log.Printf("Error submitting transaction to mempool: %v", err)
                } else {
                    log.Printf("Transaction %s successfully submitted to mempool", eip1559TxHash)
                }
            }()
		}
	}        
        c.JSON(http.StatusOK, response)
}

// Global host variable to store the libp2p host instance for network operations
var globalHost host.Host

// SetHostInstance sets the global host instance for transaction propagation
func SetHostInstance(h host.Host) {
    globalHost = h
}


func Startserver(port int, h host.Host) {
    // Open or create a log file in append mode
    f, err := os.OpenFile("logs/transactions.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
    if err != nil {
        log.Fatalf("Unable to open log file: %v", err)
    }
    defer f.Close()
    
    log.SetOutput(f)
    gin.DefaultWriter = f
    gin.DefaultErrorWriter = f

    router := gin.Default()
    SetHostInstance(h)
    // Configure CORS if needed
    router.Use(func(c *gin.Context) {
        c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
        c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
        c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, Authorization")
        if c.Request.Method == "OPTIONS" {
            c.AbortWithStatus(204)
            return
        }
        c.Next()
    })
    
    // Transaction endpoints
    router.POST("/api/generate-tx", generateTransactions)
    router.POST("/api/process-block", processZKBlock)
    router.GET("/api/block/:number", getBlockByNumber)
    router.GET("/api/block/hash/:hash", getBlockByHash)
    router.GET("/api/tx/:hash", getTransactionInfo)
    router.GET("/api/latest-block", getLatestBlock)
    
    // Add a health check endpoint
    router.GET("/health", func(c *gin.Context) {
        c.JSON(http.StatusOK, gin.H{"status": "ok"})
    })
    

    // Start server
    portStr := fmt.Sprintf(":%d", port)
    log.Println("Starting transaction generator API on", portStr)
    if err := router.Run(portStr); err != nil {
        log.Fatalf("Failed to start server: %v", err)
    }
}

func processZKBlock(c *gin.Context) {
    // Parse the block data from the request
    var block config.ZKBlock
    if err := c.ShouldBindJSON(&block); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid block data: %v", err)})
        return
    }
    
    // Validate block data
    if len(block.Transactions) == 0 {
        c.JSON(http.StatusBadRequest, gin.H{"error": "block contains no transactions"})
        return
    }
    
    if block.Status != "verified" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "block has not been verified by ZKVM"})
        return
    }
    

    if err := messaging.PropagateZKBlock(globalHost, &block); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{
            "error": fmt.Sprintf("failed to process/propagate block: %v", err),
        })
        return
    }
    
    // Return success
    c.JSON(http.StatusOK, gin.H{
        "status": "success",
        "message": fmt.Sprintf("Block %d with %d transactions processed and propagated successfully", 
            block.BlockNumber, len(block.Transactions)),
        "block_hash": block.BlockHash.Hex(),
        "block_number": block.BlockNumber,
    })
}

// getBlockByNumber retrieves a block by its number
func getBlockByNumber(c *gin.Context) {
    blockNumberStr := c.Param("number")
    blockNumber, err := strconv.ParseUint(blockNumberStr, 10, 64)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid block number"})
        return
    }
    
    mainDBClient, err := DB_OPs.New()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
        return
    }
    defer DB_OPs.Close(mainDBClient)
    
    block, err := DB_OPs.GetZKBlockByNumber(mainDBClient, blockNumber)
    if err != nil {
        c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("block not found: %v", err)})
        return
    }
    
    c.JSON(http.StatusOK, block)
}

// getBlockByHash retrieves a block by its hash
func getBlockByHash(c *gin.Context) {
    blockHash := c.Param("hash")
    
    mainDBClient, err := DB_OPs.New()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
        return
    }
    defer DB_OPs.Close(mainDBClient)
    
    block, err := DB_OPs.GetZKBlockByHash(mainDBClient, blockHash)
    if err != nil {
        c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("block not found: %v", err)})
        return
    }
    
    c.JSON(http.StatusOK, block)
}

// getTransactionInfo gets detailed information about a transaction
func getTransactionInfo(c *gin.Context) {
    txHash := c.Param("hash")
    
    mainDBClient, err := DB_OPs.New()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
        return
    }
    defer DB_OPs.Close(mainDBClient)
    
    block, err := DB_OPs.GetTransactionBlock(mainDBClient, txHash)
    if err != nil {
        c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("transaction not found: %v", err)})
        return
    }
    
    // Find the specific transaction
    var foundTx *config.ZKBlockTransaction
    for _, tx := range block.Transactions {
        if tx.Hash == txHash {
            foundTx = &tx
            break
        }
    }
    
    if foundTx == nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "transaction found in block but details missing"})
        return
    }
    
    // Return transaction with block details
    c.JSON(http.StatusOK, gin.H{
        "transaction": foundTx,
        "block_number": block.BlockNumber,
        "block_hash": block.BlockHash.Hex(),
        "timestamp": block.Timestamp,
        "confirmed": true,
    })
}

// getLatestBlock returns information about the latest block
func getLatestBlock(c *gin.Context) {
    mainDBClient, err := DB_OPs.New()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
        return
    }
    defer DB_OPs.Close(mainDBClient)
    
    latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(mainDBClient)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get latest block: %v", err)})
        return
    }
    
    if latestBlockNumber == 0 {
        c.JSON(http.StatusOK, gin.H{"message": "no blocks in the chain yet"})
        return
    }
    
    block, err := DB_OPs.GetZKBlockByNumber(mainDBClient, latestBlockNumber)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get latest block data: %v", err)})
        return
    }
    
    c.JSON(http.StatusOK, block)
}