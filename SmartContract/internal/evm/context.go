package evm

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

// BlockHashManager manages block hash retrieval and caching
type BlockHashManager struct {
	cache       map[uint64]common.Hash
	cacheMutex  sync.RWMutex
	apiEndpoint string
}

var (
	defaultManager = &BlockHashManager{
		cache:       make(map[uint64]common.Hash),
		apiEndpoint: "http://localhost:8090",
	}
)

// SetAPIEndpoint allows changing the default API endpoint
func SetAPIEndpoint(endpoint string) {
	defaultManager.apiEndpoint = endpoint
}

// GetHashFn returns the hash of the block at the specified height
// It implements vm.GetHashFunc
func GetHashFn(n uint64) common.Hash {
	return defaultManager.GetHash(n)
}

// GetHash implements the caching logic for block hashes
func (m *BlockHashManager) GetHash(n uint64) common.Hash {
	// Check cache first
	m.cacheMutex.RLock()
	cachedHash, found := m.cache[n]
	m.cacheMutex.RUnlock()

	if found {
		return cachedHash
	}

	// Not in cache, try to fetch from API
	hash, err := m.fetchBlockHashFromAPI(n)
	if err == nil {
		// Cache the result
		m.cacheMutex.Lock()
		m.cache[n] = hash
		m.cacheMutex.Unlock()
		return hash
	}

	// Fallback to deterministic hash on error
	// This ensures execution doesn't panic on network errors, but isn't ideal for mainnet
	return common.BytesToHash(crypto.Keccak256([]byte(fmt.Sprintf("%d", n))))
}

func (m *BlockHashManager) fetchBlockHashFromAPI(number uint64) (common.Hash, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("%s/api/block/%d", m.apiEndpoint, number)

	resp, err := client.Get(url)
	if err != nil {
		return common.Hash{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return common.Hash{}, fmt.Errorf("status %d", resp.StatusCode)
	}

	var response struct {
		Block struct {
			BlockHash common.Hash `json:"block_hash"`
		} `json:"block"`
		Error string `json:"error,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return common.Hash{}, err
	}
	if response.Error != "" {
		return common.Hash{}, fmt.Errorf("%s", response.Error)
	}

	return response.Block.BlockHash, nil
}

// UpdateBlockContext updates the block context with latest chain info
func UpdateBlockContext(blockCtx *vm.BlockContext) error {
	return defaultManager.UpdateBlockContext(blockCtx)
}

func (m *BlockHashManager) UpdateBlockContext(blockCtx *vm.BlockContext) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("%s/api/latest-block", m.apiEndpoint))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	var response struct {
		Block struct {
			BlockNumber  uint64 `json:"block_number"`
			Timestamp    uint64 `json:"timestamp"`
			GasLimit     uint64 `json:"gas_limit"`
			CoinbaseAddr string `json:"coinbase_addr"`
		} `json:"block"`
		Error string `json:"error,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return err
	}
	if response.Error != "" {
		return fmt.Errorf("%s", response.Error)
	}

	// Update block context with real values
	fmt.Printf("DEBUG: UpdateBlockContext fetching from API... GasLimit from API: %d\n", response.Block.GasLimit)
	blockCtx.BlockNumber = new(big.Int).SetUint64(response.Block.BlockNumber)
	// Only update time if the API returns a valid timestamp (>0).
	// If it returns 0 (e.g. genesis), we keep the default time.Now() to ensure Shanghai is active.
	if response.Block.Timestamp > 0 {
		blockCtx.Time = response.Block.Timestamp
	} else {
		fmt.Printf("DEBUG: API returned Timestamp 0, keeping default time: %d\n", blockCtx.Time)
	}
	blockCtx.GasLimit = response.Block.GasLimit

	if response.Block.CoinbaseAddr != "" {
		blockCtx.Coinbase = common.HexToAddress(response.Block.CoinbaseAddr)
	}

	return nil
}

// DefaultBlockContext returns a safe default block context
func DefaultBlockContext(gasLimit uint64) vm.BlockContext {
	return vm.BlockContext{
		CanTransfer: canTransferFn,
		Transfer:    transferFn,
		GetHash:     GetHashFn,
		Coinbase:    common.Address{},
		BlockNumber: new(big.Int).SetUint64(1),
		Time:        uint64(time.Now().UTC().Unix()),
		Difficulty:  big.NewInt(0),
		GasLimit:    30_000_000, // Fixed high limit for simulated block
		BaseFee:     big.NewInt(0),
	}
}
