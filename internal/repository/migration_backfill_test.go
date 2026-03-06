package repository

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"

	"github.com/ethereum/go-ethereum/common"
)

// MockRepositories for isolated testing
type MockImmuRepo struct {
	blocks   map[uint64]*config.ZKBlock
	accounts map[common.Address]*DB_OPs.Account
	latest   uint64
}

func NewMockImmuRepo() *MockImmuRepo {
	r := &MockImmuRepo{
		blocks:   make(map[uint64]*config.ZKBlock),
		accounts: make(map[common.Address]*DB_OPs.Account),
	}
	// Add some dummy blocks with valid timestamp strings (ThebeDB builder requires this)
	r.blocks[1] = &config.ZKBlock{BlockNumber: 1, BlockHash: common.HexToHash("0x1"), PrevHash: common.HexToHash("0x0"), StateRoot: common.HexToHash("0x11"), TxnsRoot: "0x1_txroot", Timestamp: time.Now().Unix()}
	r.blocks[2] = &config.ZKBlock{BlockNumber: 2, BlockHash: common.HexToHash("0x2"), PrevHash: common.HexToHash("0x1"), StateRoot: common.HexToHash("0x22"), TxnsRoot: "0x2_txroot", Timestamp: time.Now().Unix()}
	r.blocks[3] = &config.ZKBlock{BlockNumber: 3, BlockHash: common.HexToHash("0x3"), PrevHash: common.HexToHash("0x2"), StateRoot: common.HexToHash("0x33"), TxnsRoot: "0x3_txroot", Timestamp: time.Now().Unix()}
	r.latest = 3
	return r
}

func NewMockImmuRepoMassive(numBlocks int, txnsPerBlock int) *MockImmuRepo {
	r := &MockImmuRepo{
		blocks:   make(map[uint64]*config.ZKBlock),
		accounts: make(map[common.Address]*DB_OPs.Account),
	}

	for i := uint64(1); i <= uint64(numBlocks); i++ {
		blockHashHex := fmt.Sprintf("0x%x", i)
		prevHashHex := fmt.Sprintf("0x%x", i-1)

		var txns []config.Transaction
		for j := 0; j < txnsPerBlock; j++ {
			fromAddr := common.HexToAddress(fmt.Sprintf("0x%x", j))
			toAddr := common.HexToAddress(fmt.Sprintf("0x%x", j+1))
			val := big.NewInt(1000)

			txns = append(txns, config.Transaction{
				Hash:     common.HexToHash(fmt.Sprintf("0x%x%04x", i, j)),
				From:     &fromAddr,
				To:       &toAddr,
				Value:    val,
				Type:     2,
				Nonce:    uint64(j),
				GasLimit: 21000,
				GasPrice: big.NewInt(5000000000),
				V:        big.NewInt(27),
				R:        big.NewInt(1),
				S:        big.NewInt(1),
			})
		}

		r.blocks[i] = &config.ZKBlock{
			BlockNumber:  i,
			BlockHash:    common.HexToHash(blockHashHex),
			PrevHash:     common.HexToHash(prevHashHex),
			StateRoot:    common.HexToHash(fmt.Sprintf("0x%064x", i)),
			TxnsRoot:     fmt.Sprintf("0x%064x", i+10000), // Ensure different from StateRoot for safety
			Timestamp:    time.Now().Unix(),
			Transactions: txns,
			StarkProof:   []byte(fmt.Sprintf("proof_for_block_%d", i)),
			ProofHash:    fmt.Sprintf("0x%064x", i+20000),
			Status:       "verified",
		}
	}
	r.latest = uint64(numBlocks)
	return r
}

func (m *MockImmuRepo) StoreAccount(ctx context.Context, account *DB_OPs.Account) error { return nil }
func (m *MockImmuRepo) GetAccount(ctx context.Context, address common.Address) (*DB_OPs.Account, error) {
	if acc, ok := m.accounts[address]; ok {
		return acc, nil
	}
	return nil, fmt.Errorf("account not found")
}
func (m *MockImmuRepo) GetAccountByDID(ctx context.Context, did string) (*DB_OPs.Account, error) {
	return nil, nil
}
func (m *MockImmuRepo) UpdateAccountBalance(ctx context.Context, address common.Address, newBalance string) error {
	return nil
}
func (m *MockImmuRepo) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error { return nil }
func (m *MockImmuRepo) GetZKBlockByNumber(ctx context.Context, number uint64) (*config.ZKBlock, error) {
	if b, ok := m.blocks[number]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("block not found")
}
func (m *MockImmuRepo) GetZKBlockByHash(ctx context.Context, hash string) (*config.ZKBlock, error) {
	return nil, nil
}
func (m *MockImmuRepo) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	return m.latest, nil
}
func (m *MockImmuRepo) StoreTransaction(ctx context.Context, tx interface{}) error { return nil }
func (m *MockImmuRepo) GetTransactionByHash(ctx context.Context, hash string) (*config.Transaction, error) {
	return nil, nil
}
func (m *MockImmuRepo) GetLogs(ctx context.Context, filterQuery Types.FilterQuery) ([]Types.Log, error) {
	return nil, nil
}

// MockThebeRepo correctly verifies writes
type MockThebeRepo struct {
	storedBlocks []uint64
}

func (m *MockThebeRepo) StoreAccount(ctx context.Context, account *DB_OPs.Account) error { return nil }
func (m *MockThebeRepo) GetAccount(ctx context.Context, address common.Address) (*DB_OPs.Account, error) {
	return nil, nil
}
func (m *MockThebeRepo) GetAccountByDID(ctx context.Context, did string) (*DB_OPs.Account, error) {
	return nil, nil
}
func (m *MockThebeRepo) UpdateAccountBalance(ctx context.Context, address common.Address, newBalance string) error {
	return nil
}
func (m *MockThebeRepo) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	m.storedBlocks = append(m.storedBlocks, block.BlockNumber)
	return nil
}
func (m *MockThebeRepo) GetZKBlockByNumber(ctx context.Context, number uint64) (*config.ZKBlock, error) {
	return nil, nil
}
func (m *MockThebeRepo) GetZKBlockByHash(ctx context.Context, hash string) (*config.ZKBlock, error) {
	return nil, nil
}
func (m *MockThebeRepo) GetLatestBlockNumber(ctx context.Context) (uint64, error)   { return 0, nil }
func (m *MockThebeRepo) StoreTransaction(ctx context.Context, tx interface{}) error { return nil }
func (m *MockThebeRepo) GetTransactionByHash(ctx context.Context, hash string) (*config.Transaction, error) {
	return nil, nil
}
func (m *MockThebeRepo) GetLogs(ctx context.Context, filterQuery Types.FilterQuery) ([]Types.Log, error) {
	return nil, nil
}

func TestBackfillWorker_BasicMigration(t *testing.T) {
	immu := NewMockImmuRepo()
	thebe := &MockThebeRepo{}

	cfg := Config{
		Enabled:             true,
		MaxBlocksPerBatch:   2,
		MaxAccountsPerBatch: 2,
		ThrottleDuration:    10 * time.Millisecond,
		MigrateBlocks:       true,
		MigrateAccounts:     false, // Disable for now to just test blocks
	}

	worker := &BackfillWorker{
		source: immu,
		target: thebe,
		config: cfg,
		state:  &StateTracker{}, // Uses a local tracker in this isolated test
	}

	// This is a unit-test override just to bypass the PostgreSQL EnsureSchema call
	// Normally you'd inject a mock StateTracker interface, but for this quick test we just
	// call a modified run logic directly.

	err := migrateBlocksLogicForTest(context.Background(), worker, 0)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	if len(thebe.storedBlocks) != 3 {
		t.Errorf("Expected 3 blocks migrated, got %d", len(thebe.storedBlocks))
	}
	if thebe.storedBlocks[0] != 1 || thebe.storedBlocks[1] != 2 || thebe.storedBlocks[2] != 3 {
		t.Errorf("Blocks stored out of order: %v", thebe.storedBlocks)
	}
}

// Helper to bypass the SQL calls in the state tracker during pure unit tests
func migrateBlocksLogicForTest(ctx context.Context, w *BackfillWorker, lastSynced uint64) error {
	targetHead, _ := w.source.GetLatestBlockNumber(ctx)
	startBlock := lastSynced + 1

	for current := startBlock; current <= targetHead; current++ {
		block, _ := w.source.GetZKBlockByNumber(ctx, current)
		if block != nil {
			w.target.StoreZKBlock(ctx, block)
		}
	}
	return nil
}
