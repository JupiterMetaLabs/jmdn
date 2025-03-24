package Block

import (
	"fmt"
	"log"
	"math/big"
	"net/http"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
)
type APIAccessTuple struct {
    Address     string   `json:"address"`
    StorageKeys []string `json:"storage_keys"`
}
// Request and response models
type TransactionRequest struct {
    RecipientAddress  string   `json:"recipient_address" binding:"required"`
    Amount            string   `json:"amount" binding:"required"`         // String to preserve precision
    Nonce             uint64   `json:"nonce" binding:"required"`
    GasLimit          uint64   `json:"gas_limit" binding:"required"`
    GasPrice          string   `json:"gas_price" binding:"required"`      // String to preserve precision
    Data              string   `json:"data"`                              // Optional data
    MaxPriorityFee    string   `json:"max_priority_fee"`                  // Optional for EIP-1559
    MaxFee            string   `json:"max_fee"`                           // Optional for EIP-1559
    ChainID           int64    `json:"chain_id" binding:"required"`
    AccessList        []APIAccessTuple `json:"access_list"`                  // Optional
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
func toBlockAccessList(apiList []APIAccessTuple) AccessList {
    if len(apiList) == 0 {
        return AccessList{}
    }
    
    result := make(AccessList, len(apiList))
    for i, tuple := range apiList {
        storageKeys := make([]common.Hash, len(tuple.StorageKeys))
        for j, key := range tuple.StorageKeys {
            storageKeys[j] = common.HexToHash(key)
        }
        
        result[i] = AccessTuple{
            Address:     common.HexToAddress(tuple.Address),
            StorageKeys: storageKeys,
        }
    }
    return result
}

// Convert Block.Transaction to TransactionData
func toTransactionData(tx *Transaction) *TransactionData {
    result := &TransactionData{
        ChainID:  tx.ChainID.String(),
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
		}
	}
    c.JSON(http.StatusOK, response)
}

func Startserver(port int) {
    router := gin.Default()
    
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