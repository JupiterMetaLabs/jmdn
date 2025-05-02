package SmartContract

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
)

// Add a block cache to avoid repeated API calls
var (
    blockHashCache     = make(map[uint64]common.Hash)
    blockHashCacheMutex = sync.RWMutex{}
    apiEndpoint        = "http://localhost:8090" // Default API endpoint
)

// SetAPIEndpoint allows changing the default API endpoint
func SetAPIEndpoint(endpoint string) {
    apiEndpoint = endpoint
}

// GetHashFn returns the hash of the block at the specified height
func GetHashFn(n uint64) common.Hash {
    // Check cache first
    blockHashCacheMutex.RLock()
    cachedHash, found := blockHashCache[n]
    blockHashCacheMutex.RUnlock()
    
    if found {
        return cachedHash
    }
    
    // Not in cache, try to fetch from API
    hash, err := fetchBlockHashFromAPI(n)
    if err == nil {
        // Cache the result
        blockHashCacheMutex.Lock()
        blockHashCache[n] = hash
        blockHashCacheMutex.Unlock()
        return hash
    }

    // Fallback to deterministic hash on error
    fallbackHash := common.BytesToHash(crypto.Keccak256([]byte(fmt.Sprintf("%d", n))))
    return fallbackHash
}

// fetchBlockHashFromAPI retrieves a block hash from the blockchain API
func fetchBlockHashFromAPI(number uint64) (common.Hash, error) {
    // Create HTTP client with timeout
    client := &http.Client{
        Timeout: 2 * time.Second, // Short timeout to avoid blocking EVM execution
    }
    
    // Make API request
    url := fmt.Sprintf("%s/api/block/%d", apiEndpoint, number)
    resp, err := client.Get(url)
    if err != nil {
        return common.Hash{}, fmt.Errorf("API request failed: %w", err)
    }
    defer resp.Body.Close()
    
    // Check status code
    if resp.StatusCode != http.StatusOK {
        return common.Hash{}, fmt.Errorf("API returned status %d", resp.StatusCode)
    }
    
    // Parse response
    var response struct {
        Block struct {
            BlockHash common.Hash `json:"block_hash"`
        } `json:"block"`
        Error string `json:"error,omitempty"`
    }
    
    if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
        return common.Hash{}, fmt.Errorf("failed to parse API response: %w", err)
    }
    
    if response.Error != "" {
        return common.Hash{}, fmt.Errorf("API error: %s", response.Error)
    }
    
    return response.Block.BlockHash, nil
}

// Add this method to EVMExecutor to update block context with latest block info
func (e *EVMExecutor) UpdateBlockContext(blockCtx *vm.BlockContext) error {
    // Fetch latest block information
    client := &http.Client{
        Timeout: 2 * time.Second,
    }
    
    resp, err := client.Get(fmt.Sprintf("%s/api/latest-block", apiEndpoint))
    if err != nil {
        return fmt.Errorf("failed to fetch latest block: %w", err)
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("API returned status %d", resp.StatusCode)
    }
    
    var response struct {
        Block struct {
            BlockNumber uint64      `json:"block_number"`
            Timestamp   uint64      `json:"timestamp"`
            GasLimit    uint64      `json:"gas_limit"`
            CoinbaseAddr string     `json:"coinbase_addr"`
        } `json:"block"`
        Error string `json:"error,omitempty"`
    }
    
    if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
        return fmt.Errorf("failed to parse API response: %w", err)
    }
    
    if response.Error != "" {
        return fmt.Errorf("API error: %s", response.Error)
    }
    
    // Update block context with real values
    blockCtx.BlockNumber = new(big.Int).SetUint64(response.Block.BlockNumber)
    blockCtx.Time = response.Block.BlockNumber
    blockCtx.GasLimit = response.Block.GasLimit
    
    // Parse coinbase address if available
    if response.Block.CoinbaseAddr != "" {
        blockCtx.Coinbase = common.HexToAddress(response.Block.CoinbaseAddr)
    }
    
    return nil
}