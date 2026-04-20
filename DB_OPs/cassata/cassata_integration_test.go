package cassata_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	thebecfg "github.com/JupiterMetaLabs/ThebeDB/pkg/config"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/profile"

	DB_OPs "gossipnode/DB_OPs"
	"gossipnode/DB_OPs/cassata"
	"gossipnode/DB_OPs/thebeprofile"
)

// helpers

func dsn(t *testing.T) string {
	t.Helper()
	d := os.Getenv("TEST_THEBE_SQL_DSN")
	if d == "" {
		t.Skip("TEST_THEBE_SQL_DSN not set")
	}
	return d
}

func newTestCassata(t *testing.T) (*cassata.Cassata, func()) {
	t.Helper()
	reg := profile.NewRegistry()
	reg.Register(thebeprofile.New())
	db, err := thebedb.NewFromConfig(thebedb.Config{
		KV:       kv.Config{Backend: kv.BackendBadger, Path: t.TempDir() + "/kv"},
		SQL:      thebecfg.SQL{DSN: dsn(t)},
		Profiles: reg,
	})
	if err != nil {
		t.Fatalf("thebedb.NewFromConfig: %v", err)
	}
	return cassata.New(db, nil), func() { db.Close() }
}

// fixture helpers - all field types match schema exactly

func testAccount(addr string) cassata.AccountResult {
	did := "did:jmdt:8000800:" + addr
	return cassata.AccountResult{
		Address:     addr,
		DIDAddress:  &did,
		BalanceWei:  "1000000000000000000", // 1 ETH as NUMERIC(78,0) string
		Nonce:       "0",
		AccountType: 0, // int16: 0=EOA
		Metadata:    json.RawMessage(`{}`),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
}

func TestCassata_GetAccountByDID(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	acct := testAccount("0x1111111111111111111111111111111111111111")
	if err := c.IngestAccount(ctx, acct); err != nil {
		t.Fatalf("IngestAccount: %v", err)
	}

	got, err := c.GetAccountByDID(ctx, *acct.DIDAddress)
	if err != nil {
		t.Fatalf("GetAccountByDID: %v", err)
	}
	if got.Address != acct.Address {
		t.Fatalf("address mismatch: want %s got %s", acct.Address, got.Address)
	}
}

func TestCassata_ListAccounts(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	addresses := []string{
		"0x7777777777777777777777777777777777777777",
		"0x8888888888888888888888888888888888888888",
		"0x9999999999999999999999999999999999999998",
	}
	for i, addr := range addresses {
		acct := testAccount(addr)
		if err := c.IngestAccount(ctx, acct); err != nil {
			t.Fatalf("IngestAccount %d: %v", i, err)
		}
	}

	got, err := c.ListAccounts(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(got) < 3 {
		t.Fatalf("expected at least 3 accounts, got %d", len(got))
	}
}

func testBlock(n uint64, hash, parent string) cassata.BlockResult {
	return cassata.BlockResult{
		BlockNumber: n,
		BlockHash:   hash,
		ParentHash:  parent,
		Timestamp:   time.Now().UTC(),
		Status:      1, // int16: 1=Confirmed
		ExtraData:   json.RawMessage(`{}`),
	}
}

// Test: account ingest + read

func TestCassata_AccountIngestAndRead(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	acct := testAccount("0xaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaA")
	if err := c.IngestAccount(ctx, acct); err != nil {
		t.Fatalf("IngestAccount: %v", err)
	}

	got, err := c.GetAccount(ctx, acct.Address)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.Address != acct.Address {
		t.Errorf("address: want %s got %s", acct.Address, got.Address)
	}
	if got.BalanceWei != acct.BalanceWei {
		t.Errorf("balance_wei: want %s got %s", acct.BalanceWei, got.BalanceWei)
	}
	if got.Nonce != acct.Nonce {
		t.Errorf("nonce: want %s got %s", acct.Nonce, got.Nonce)
	}
	if got.AccountType != acct.AccountType {
		t.Errorf("account_type: want %d got %d", acct.AccountType, got.AccountType)
	}
}

// Test: account balance update (mutable)

func TestCassata_AccountBalanceUpdate(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	addr := "0xbBbBbBbBbBbBbBbBbBbBbBbBbBbBbBbBbBbBbBbB"
	if err := c.IngestAccount(ctx, testAccount(addr)); err != nil {
		t.Fatalf("initial IngestAccount: %v", err)
	}

	// Update balance - accounts table is mutable (CUR)
	updated := testAccount(addr)
	updated.BalanceWei = "2000000000000000000"
	updated.Nonce = "1"
	if err := c.IngestAccount(ctx, updated); err != nil {
		t.Fatalf("update IngestAccount: %v", err)
	}

	got, err := c.GetAccount(ctx, addr)
	if err != nil {
		t.Fatalf("GetAccount after update: %v", err)
	}
	if got.BalanceWei != "2000000000000000000" {
		t.Errorf("balance_wei after update: want 2000000000000000000 got %s", got.BalanceWei)
	}
	if got.Nonce != "1" {
		t.Errorf("nonce after update: want 1 got %s", got.Nonce)
	}
}

// Test: block ingest + read

func TestCassata_BlockIngestAndRead(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	block := testBlock(
		1,
		"0x1111111111111111111111111111111111111111111111111111111111111111",
		"0x0000000000000000000000000000000000000000000000000000000000000000",
	)
	if err := c.IngestBlock(ctx, block); err != nil {
		t.Fatalf("IngestBlock: %v", err)
	}

	got, err := c.GetBlock(ctx, 1)
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if got.BlockNumber != 1 {
		t.Errorf("block_number: want 1 got %d", got.BlockNumber)
	}
	if got.BlockHash != block.BlockHash {
		t.Errorf("block_hash: want %s got %s", block.BlockHash, got.BlockHash)
	}
	if got.Status != 1 {
		t.Errorf("status: want 1 got %d", got.Status)
	}
}

// Test: blocks are append-only (duplicate insert ignored)

func TestCassata_BlockAppendOnly(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	block := testBlock(
		10,
		"0xaaaa000000000000000000000000000000000000000000000000000000000001",
		"0x0000000000000000000000000000000000000000000000000000000000000000",
	)
	if err := c.IngestBlock(ctx, block); err != nil {
		t.Fatalf("first IngestBlock: %v", err)
	}
	// Second insert of same block_number must not error (ON CONFLICT DO NOTHING)
	if err := c.IngestBlock(ctx, block); err != nil {
		t.Fatalf("duplicate IngestBlock must not error: %v", err)
	}
}

// Test: transaction ingest + read (satisfies FK deps)

func TestCassata_TxIngestAndRead(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	// 1. accounts first (FK: from_addr -> accounts.address)
	fromAddr := "0xaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaAaA"
	if err := c.IngestAccount(ctx, testAccount(fromAddr)); err != nil {
		t.Fatalf("IngestAccount: %v", err)
	}

	// 2. block second (FK: block_number -> blocks.block_number)
	if err := c.IngestBlock(ctx, testBlock(
		2,
		"0x2222222222222222222222222222222222222222222222222222222222222222",
		"0x0000000000000000000000000000000000000000000000000000000000000000",
	)); err != nil {
		t.Fatalf("IngestBlock: %v", err)
	}

	// 3. transaction last
	gasPrice := "21000000000"
	tx := cassata.TxResult{
		TxHash:      "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		BlockNumber: 2,
		TxIndex:     0,
		FromAddr:    fromAddr,
		ToAddr:      nil, // contract creation
		ValueWei:    "0",
		Nonce:       "0",
		Type:        0, // int16: 0=Legacy
		GasPriceWei: &gasPrice,
		AccessList:  json.RawMessage(`[]`),
	}
	if err := c.IngestTx(ctx, tx); err != nil {
		t.Fatalf("IngestTx: %v", err)
	}

	got, err := c.GetTransaction(ctx, tx.TxHash)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if got.TxHash != tx.TxHash {
		t.Errorf("tx_hash mismatch")
	}
	if got.BlockNumber != 2 {
		t.Errorf("block_number: want 2 got %d", got.BlockNumber)
	}
	if got.Type != 0 {
		t.Errorf("type: want 0 got %d", got.Type)
	}
	if got.ToAddr != nil {
		t.Errorf("to_addr: want nil (contract creation) got %v", *got.ToAddr)
	}
}

// Test: ZK proof ingest + read

func TestCassata_ZKProofIngestAndRead(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	// block must exist first (FK)
	if err := c.IngestBlock(ctx, testBlock(
		20,
		"0x2020202020202020202020202020202020202020202020202020202020202020",
		"0x0000000000000000000000000000000000000000000000000000000000000000",
	)); err != nil {
		t.Fatalf("IngestBlock: %v", err)
	}

	zk := cassata.ZKProofResult{
		ProofHash:   "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		BlockNumber: 20,
		StarkProof:  []byte("fake-stark-proof-bytes"),
		Commitment:  []byte("fake-commitment"),
	}
	if err := c.IngestZKProof(ctx, zk); err != nil {
		t.Fatalf("IngestZKProof: %v", err)
	}

	got, err := c.GetZKProofByBlock(ctx, 20)
	if err != nil {
		t.Fatalf("GetZKProofByBlock: %v", err)
	}
	if got.ProofHash != zk.ProofHash {
		t.Errorf("proof_hash mismatch")
	}
	if string(got.StarkProof) != string(zk.StarkProof) {
		t.Errorf("stark_proof mismatch")
	}

	gotByHash, err := c.GetZKProof(ctx, zk.ProofHash)
	if err != nil {
		t.Fatalf("GetZKProof: %v", err)
	}
	if gotByHash.BlockNumber != zk.BlockNumber {
		t.Fatalf("proof block mismatch: want %d got %d", zk.BlockNumber, gotByHash.BlockNumber)
	}
}

// Test: snapshot ingest + read (with chain)

func TestCassata_SnapshotIngestAndRead(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	// block must exist first (FK)
	if err := c.IngestBlock(ctx, testBlock(
		30,
		"0x3030303030303030303030303030303030303030303030303030303030303030",
		"0x0000000000000000000000000000000000000000000000000000000000000000",
	)); err != nil {
		t.Fatalf("IngestBlock: %v", err)
	}

	// genesis snapshot (prev_snapshot_id = nil)
	snap := cassata.SnapshotResult{
		BlockNumber:    30,
		BlockHash:      "0x3030303030303030303030303030303030303030303030303030303030303030",
		PrevSnapshotID: nil,
	}
	if err := c.IngestSnapshot(ctx, snap); err != nil {
		t.Fatalf("IngestSnapshot: %v", err)
	}

	got, err := c.GetSnapshot(ctx, 30)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if got.BlockNumber != 30 {
		t.Errorf("block_number: want 30 got %d", got.BlockNumber)
	}
	if got.PrevSnapshotID != nil {
		t.Errorf("prev_snapshot_id: want nil (genesis) got %v", *got.PrevSnapshotID)
	}
	if got.SnapshotID == 0 {
		t.Error("snapshot_id should be auto-generated and non-zero")
	}

	snaps, err := c.ListSnapshots(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) == 0 {
		t.Fatal("expected at least one snapshot")
	}
}

// Test: ListBlocks pagination

func TestCassata_ListBlocks(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	hashes := []string{
		"0xaaaa000000000000000000000000000000000000000000000000000000000010",
		"0xaaaa000000000000000000000000000000000000000000000000000000000011",
		"0xaaaa000000000000000000000000000000000000000000000000000000000012",
	}
	parent := "0x0000000000000000000000000000000000000000000000000000000000000000"
	for i, h := range hashes {
		if err := c.IngestBlock(ctx, testBlock(uint64(100+i), h, parent)); err != nil {
			t.Fatalf("IngestBlock %d: %v", i, err)
		}
		parent = h
	}

	blocks, err := c.ListBlocks(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListBlocks: %v", err)
	}
	if len(blocks) < 3 {
		t.Errorf("expected at least 3 blocks, got %d", len(blocks))
	}
	// ListBlocks orders by block_number DESC
	if blocks[0].BlockNumber < blocks[len(blocks)-1].BlockNumber {
		t.Error("ListBlocks should return DESC order")
	}
}

func TestCassata_TransactionReadPaths(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	fromAddr := "0x2222222222222222222222222222222222222222"
	toAddr := "0x3333333333333333333333333333333333333333"
	if err := c.IngestAccount(ctx, testAccount(fromAddr)); err != nil {
		t.Fatalf("IngestAccount(from): %v", err)
	}
	if err := c.IngestAccount(ctx, testAccount(toAddr)); err != nil {
		t.Fatalf("IngestAccount(to): %v", err)
	}
	if err := c.IngestBlock(ctx, testBlock(
		200,
		"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa00",
		"0x0000000000000000000000000000000000000000000000000000000000000000",
	)); err != nil {
		t.Fatalf("IngestBlock: %v", err)
	}

	to := toAddr
	gasPrice := "21000000000"
	tx := cassata.TxResult{
		TxHash:      "0x9999999999999999999999999999999999999999999999999999999999999999",
		BlockNumber: 200,
		TxIndex:     0,
		FromAddr:    fromAddr,
		ToAddr:      &to,
		ValueWei:    "1",
		Nonce:       "7",
		Type:        0,
		GasPriceWei: &gasPrice,
		AccessList:  json.RawMessage(`[]`),
	}
	if err := c.IngestTx(ctx, tx); err != nil {
		t.Fatalf("IngestTx: %v", err)
	}

	byBlock, err := c.ListTransactionsByBlock(ctx, 200)
	if err != nil {
		t.Fatalf("ListTransactionsByBlock: %v", err)
	}
	if len(byBlock) == 0 {
		t.Fatal("expected transactions by block")
	}

	byAddr, err := c.ListTransactionsByAddress(ctx, fromAddr, 10, 0)
	if err != nil {
		t.Fatalf("ListTransactionsByAddress: %v", err)
	}
	if len(byAddr) == 0 {
		t.Fatal("expected transactions by address")
	}

	nextNonce, err := c.GetNextNonceByAddress(ctx, fromAddr)
	if err != nil {
		t.Fatalf("GetNextNonceByAddress: %v", err)
	}
	if nextNonce != "8" {
		t.Fatalf("next nonce mismatch: want 8 got %s", nextNonce)
	}
}

func TestCassata_BatchWritePath_WithShadowAdapter(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	adapter := cassata.NewShadowAdapter(c)
	addr1 := "0x4444444444444444444444444444444444444444"
	addr2 := "0x5555555555555555555555555555555555555555"
	a1 := testAccount(addr1)
	a2 := testAccount(addr2)

	entries := map[string]interface{}{
		DB_OPs.Prefix + addr1: a1,
		DB_OPs.Prefix + addr2: a2,
	}
	if err := adapter.BatchCreate(nil, entries); err != nil {
		t.Fatalf("BatchCreate: %v", err)
	}

	if _, err := c.GetAccount(ctx, addr1); err != nil {
		t.Fatalf("GetAccount addr1: %v", err)
	}
	if _, err := c.GetAccount(ctx, addr2); err != nil {
		t.Fatalf("GetAccount addr2: %v", err)
	}
}

func TestImmutabilityConstraints(t *testing.T) {
	c, cleanup := newTestCassata(t)
	defer cleanup()
	ctx := context.Background()

	// Immutable path: duplicate block insert should be ignored (append-only semantics).
	block := testBlock(
		900,
		"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa90",
		"0x0000000000000000000000000000000000000000000000000000000000000000",
	)
	if err := c.IngestBlock(ctx, block); err != nil {
		t.Fatalf("first IngestBlock: %v", err)
	}
	if err := c.IngestBlock(ctx, block); err != nil {
		t.Fatalf("second IngestBlock should not fail: %v", err)
	}

	// Mutable path: account updates must remain allowed.
	addr := "0x6666666666666666666666666666666666666666"
	acct := testAccount(addr)
	if err := c.IngestAccount(ctx, acct); err != nil {
		t.Fatalf("IngestAccount initial: %v", err)
	}
	acct.BalanceWei = "42"
	acct.Nonce = "2"
	if err := c.IngestAccount(ctx, acct); err != nil {
		t.Fatalf("IngestAccount update: %v", err)
	}
	got, err := c.GetAccount(ctx, addr)
	if err != nil {
		t.Fatalf("GetAccount after update: %v", err)
	}
	if got.BalanceWei != "42" || got.Nonce != "2" {
		t.Fatalf("mutable account update failed: balance=%s nonce=%s", got.BalanceWei, got.Nonce)
	}
}
