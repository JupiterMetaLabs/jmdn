package backend

// D-64 regression tests: StoreZKBlock must write the `snapshots` record for
// EVERY block, proof or not, because snapshots is the FK parent of transactions
// (fk_txn_snapshot). The pre-fix code gated the snapshot write on the presence of
// a ZK proof, so a proofless block — every catch-up block, whose served copy
// carries no proof — wrote a `blocks` row, NO `snapshots` row, and every
// transaction insert then violated the FK (23503) and was lost to the outbox.
//
// The ZK proof write, in contrast, stays conditional: zk_proofs.proof_hash is
// CHAR(66) NOT NULL UNIQUE, so a proofless block must NOT write an empty proof.

import (
	"context"
	"testing"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// countingGateway is a thebegateway.ThebeGateway spy that counts the write calls
// StoreZKBlock makes. Every other method is an unused no-op.
type countingGateway struct {
	blocks    int
	snapshots int
	zkproofs  int
	txs       int
}

func (g *countingGateway) WriteBlock(_ context.Context, _ *thebegateway.BlockRecord) error {
	g.blocks++
	return nil
}
func (g *countingGateway) WriteSnapshot(_ context.Context, _ *thebegateway.SnapshotRecord) error {
	g.snapshots++
	return nil
}
func (g *countingGateway) WriteZKProof(_ context.Context, _ *thebegateway.ZKProofRecord) error {
	g.zkproofs++
	return nil
}
func (g *countingGateway) WriteTransaction(_ context.Context, _ *thebegateway.TransactionRecord) error {
	g.txs++
	return nil
}

// Remaining ThebeGateway methods — unused no-ops so the spy satisfies the interface.
func (g *countingGateway) WriteAccount(_ context.Context, _ *thebegateway.AccountRecord) error {
	return nil
}
func (g *countingGateway) WriteL1Finality(_ context.Context, _ *thebegateway.L1FinalityRecord) error {
	return nil
}
func (g *countingGateway) WriteContractCode(_ context.Context, _ *thebegateway.ContractCodeRecord) error {
	return nil
}
func (g *countingGateway) WriteContractNonce(_ context.Context, _ *thebegateway.ContractNonceRecord) error {
	return nil
}
func (g *countingGateway) WriteContractStorage(_ context.Context, _ *thebegateway.ContractStorageRecord) error {
	return nil
}
func (g *countingGateway) WriteContractMeta(_ context.Context, _ *thebegateway.ContractMetaRecord) error {
	return nil
}
func (g *countingGateway) WriteContractReceipt(_ context.Context, _ *thebegateway.ContractReceiptRecord) error {
	return nil
}
func (g *countingGateway) SetTxProcessing(_ context.Context, _ string) error   { return nil }
func (g *countingGateway) ClearTxProcessing(_ context.Context, _ string) error { return nil }
func (g *countingGateway) PutSyncKV(_ string, _ []byte) error                  { return nil }
func (g *countingGateway) GetSyncKV(_ string) ([]byte, error)                  { return nil, nil }

var _ thebegateway.ThebeGateway = (*countingGateway)(nil)

func TestStoreZKBlock_WritesSnapshotForProoflessBlock(t *testing.T) {
	spy := &countingGateway{}
	b := &thebeBackend{gw: spy}

	// The catch-up case: ProofHash empty, StarkProof nil.
	proofless := &config.ZKBlock{
		BlockNumber: 831,
		BlockHash:   common.HexToHash("0xbeef"),
	}
	if err := b.StoreZKBlock(context.Background(), proofless); err != nil {
		t.Fatalf("StoreZKBlock(proofless): %v", err)
	}
	if spy.blocks != 1 {
		t.Errorf("block writes = %d, want 1", spy.blocks)
	}
	if spy.snapshots != 1 {
		t.Errorf("snapshot writes = %d, want 1 — D-64: the snapshot (FK parent of transactions) must be written for every block, proof or not", spy.snapshots)
	}
	if spy.zkproofs != 0 {
		t.Errorf("zkproof writes = %d, want 0 — proof_hash is NOT NULL UNIQUE; a proofless block must not write an empty proof", spy.zkproofs)
	}
}

func TestStoreZKBlock_WritesZKProofOnlyWhenPresent(t *testing.T) {
	spy := &countingGateway{}
	b := &thebeBackend{gw: spy}

	withProof := &config.ZKBlock{
		BlockNumber: 832,
		BlockHash:   common.HexToHash("0xcafe"),
		ProofHash:   "0xabc", // non-empty ⇒ proof present
	}
	if err := b.StoreZKBlock(context.Background(), withProof); err != nil {
		t.Fatalf("StoreZKBlock(withProof): %v", err)
	}
	if spy.snapshots != 1 {
		t.Errorf("snapshot writes = %d, want 1", spy.snapshots)
	}
	if spy.zkproofs != 1 {
		t.Errorf("zkproof writes = %d, want 1 — the block carries a proof", spy.zkproofs)
	}
}
