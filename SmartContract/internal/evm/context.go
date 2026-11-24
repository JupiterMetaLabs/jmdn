package evm

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
)

// blockHashCacheMaxSize is the maximum number of entries kept in the BlockHashManager
// cache.  When the limit is reached the entire cache is cleared to prevent unbounded
// memory growth (a simple eviction strategy sufficient for a rolling block window).
const blockHashCacheMaxSize = 256

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

// HeadInfo is the header subset the loopback EVM context needs from the chain tip.
type HeadInfo struct {
	Number    uint64
	Timestamp uint64
	GasLimit  uint64
	Coinbase  common.Address
}

// ChainReader reads the local chain in-process. When set (SetChainReader), the
// loopback EVM context is built from it instead of HTTP GETs to the node's own
// Block API — which returned the FULL tip block, a ~2.66 MB STARK proof
// included, on every eth_call/estimateGas only for four header fields
// (runtime bundle 2026-10-15: 19.3 GB of JSON decode + ~16 calls/min on the
// sequencer). Head must report the same tip /api/latest-block serves (the
// data-complete tip); HashAt returns the stored hash of block n.
type ChainReader struct {
	Head   func() (HeadInfo, error)
	HashAt func(n uint64) (common.Hash, error)
}

// headCacheTTL bounds how stale the cached tip header may be. Loopback eth_call
// context only (never consensus apply — see evm_deterministic.go).
const headCacheTTL = time.Second

var (
	chainReaderMu sync.RWMutex
	chainReader   *ChainReader

	headCacheMu sync.Mutex
	headCache   HeadInfo
	headCacheAt time.Time
)

// SetChainReader installs (or, with nil, removes) the in-process chain reader.
func SetChainReader(r *ChainReader) {
	chainReaderMu.Lock()
	chainReader = r
	chainReaderMu.Unlock()
	headCacheMu.Lock()
	headCacheAt = time.Time{}
	headCacheMu.Unlock()
}

func currentChainReader() *ChainReader {
	chainReaderMu.RLock()
	defer chainReaderMu.RUnlock()
	return chainReader
}

// cachedHead returns the tip header from r, reusing a read younger than headCacheTTL.
func cachedHead(r *ChainReader) (HeadInfo, error) {
	headCacheMu.Lock()
	defer headCacheMu.Unlock()
	if !headCacheAt.IsZero() && time.Since(headCacheAt) < headCacheTTL {
		return headCache, nil
	}
	h, err := r.Head()
	if err != nil {
		return HeadInfo{}, err
	}
	headCache, headCacheAt = h, time.Now()
	return h, nil
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

	// Not in cache: in-process read when wired, else the Block API.
	var hash common.Hash
	var err error
	if r := currentChainReader(); r != nil && r.HashAt != nil {
		hash, err = r.HashAt(n)
	} else {
		hash, err = m.fetchBlockHashFromAPI(n)
	}
	if err == nil {
		// Cache the result; evict the whole map if the size cap is reached.
		m.cacheMutex.Lock()
		if len(m.cache) >= blockHashCacheMaxSize {
			m.cache = make(map[uint64]common.Hash, blockHashCacheMaxSize)
		}
		m.cache[n] = hash
		m.cacheMutex.Unlock()
		return hash
	}

	// Fallback to deterministic hash on error
	// This ensures execution doesn't panic on network errors, but isn't ideal for mainnet
	return common.BytesToHash(crypto.Keccak256(fmt.Appendf(nil, "%d", n)))
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

	// /api/block/:n serves a bare config.ZKBlock (c.JSON(200, block)), not a
	// {"block":{…}} envelope. The previous decoder read block.block_hash and so
	// always got the zero hash.
	var response struct {
		BlockHash common.Hash `json:"blockhash"`
		Error     string      `json:"error,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return common.Hash{}, err
	}
	if response.Error != "" {
		return common.Hash{}, fmt.Errorf("%s", response.Error)
	}

	return response.BlockHash, nil
}

// UpdateBlockContext updates the block context with latest chain info
func UpdateBlockContext(blockCtx *vm.BlockContext) error {
	return defaultManager.UpdateBlockContext(blockCtx)
}

func (m *BlockHashManager) UpdateBlockContext(blockCtx *vm.BlockContext) error {
	if r := currentChainReader(); r != nil && r.Head != nil {
		h, err := cachedHead(r)
		if err != nil {
			return err
		}
		applyHead(blockCtx, h)
		return nil
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("%s/api/latest-block", m.apiEndpoint))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	// /api/latest-block serves a bare config.ZKBlock (json tags blocknumber,
	// timestamp, gaslimit, coinbaseaddr), or {"message": …} on an empty chain.
	// The previous decoder expected a {"block":{block_number,…}} envelope, so
	// every field decoded as zero (block number 0, GASLIMIT 0) on every call.
	var response struct {
		BlockNumber  uint64          `json:"blocknumber"`
		Timestamp    int64           `json:"timestamp"`
		GasLimit     uint64          `json:"gaslimit"`
		CoinbaseAddr *common.Address `json:"coinbaseaddr"`
		Error        string          `json:"error,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return err
	}
	if response.Error != "" {
		return fmt.Errorf("%s", response.Error)
	}
	if response.BlockNumber == 0 {
		return fmt.Errorf("no blocks in the chain yet")
	}

	if l := evmLogger(); l != nil {
		l.Debug(context.Background(), "UpdateBlockContext from API",
			ion.Uint64("block_number", response.BlockNumber),
			ion.Uint64("gas_limit", response.GasLimit),
			ion.Int64("timestamp", response.Timestamp),
		)
	}
	h := HeadInfo{Number: response.BlockNumber, GasLimit: response.GasLimit}
	if response.Timestamp > 0 {
		h.Timestamp = uint64(response.Timestamp)
	}
	if response.CoinbaseAddr != nil {
		h.Coinbase = *response.CoinbaseAddr
	}
	applyHead(blockCtx, h)
	return nil
}

// applyHead copies the tip header into blockCtx with the original HTTP-path
// semantics: Time only when > 0 (genesis keeps time.Now() so Shanghai stays
// active), Coinbase only when non-zero, GasLimit copied as-is.
func applyHead(blockCtx *vm.BlockContext, h HeadInfo) {
	blockCtx.BlockNumber = new(big.Int).SetUint64(h.Number)
	if h.Timestamp > 0 {
		blockCtx.Time = h.Timestamp
	}
	blockCtx.GasLimit = h.GasLimit
	if h.Coinbase != (common.Address{}) {
		blockCtx.Coinbase = h.Coinbase
	}
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
		GasLimit:    30_000_000,       // Fixed high limit for simulated block
		Random:      new(common.Hash), // NEW-8/EVM-29: non-nil -> post-merge -> Shanghai (fork parity with the apply path)
		BaseFee:     big.NewInt(0),
	}
}
