package DB_OPs

// Review follow-up tests (D-65 / D-66), both sandbox-safe: they inject a spy
// store.ThebeHandle via SetGlobalHandle and exercise the real DB_OPs code, no DB.

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// --- C (D-66): StoreZKBlock must NOT recompute tx stats from the projection ---

// refreshSpyHandle counts StoreZKBlock and RefreshAccountTxStats calls; every
// other ThebeHandle method falls through to the embedded nil (never reached).
type refreshSpyHandle struct {
	store.ThebeHandle
	storeZK int
	refresh int
}

func (h *refreshSpyHandle) StoreZKBlock(_ context.Context, _ *config.ZKBlock) error {
	h.storeZK++
	return nil
}

func (h *refreshSpyHandle) RefreshAccountTxStats(_ context.Context, _ string) error {
	h.refresh++
	return nil
}

func TestStoreZKBlock_DoesNotCallRefreshAccountTxStats(t *testing.T) {
	h := &refreshSpyHandle{}
	SetGlobalHandle(h)
	defer SetGlobalHandle(nil)

	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	blk := &config.ZKBlock{
		BlockNumber:  900,
		BlockHash:    common.HexToHash("0xabc"),
		Transactions: []config.Transaction{{From: &from}},
	}
	if err := StoreZKBlock(nil, blk); err != nil {
		t.Fatalf("StoreZKBlock: %v", err)
	}
	if h.refresh != 0 {
		t.Errorf("StoreZKBlock called RefreshAccountTxStats %d times; D-66 removed it "+
			"(tx_count_sent / tx_nonce are apply-path-authoritative fingerprint fields, "+
			"never re-derived from the rebuildable transactions projection)", h.refresh)
	}
	if h.storeZK != 1 {
		t.Errorf("expected exactly one StoreZKBlock projection-chain call, got %d", h.storeZK)
	}
}

// --- B (D-65): ProofHash/StarkProof survive GetZKBlockByNumber + json round-trip ---
//
// This covers the READ+SERVE half of the catch-up path (the half the silent
// err==nil drop lived in): reconstruct via GetZKBlockByNumber, then json.Marshal
// exactly as thebesync/provider.go ships the block. The WRITE half (the proof
// reaches the zk_proofs row) is covered by backend/zkproof_snapshot_test.go.

type roundTripHandle struct {
	store.ThebeHandle
	blockRec *thebegateway.BlockRecord
	proofRec *thebegateway.ZKProofRecord
}

func (h *roundTripHandle) GetBlock(_ context.Context, _ uint64) (*thebegateway.BlockRecord, error) {
	return h.blockRec, nil
}
func (h *roundTripHandle) GetZKProof(_ context.Context, _ uint64) (*thebegateway.ZKProofRecord, error) {
	return h.proofRec, nil
}
func (h *roundTripHandle) GetTransactionsByBlock(_ context.Context, _ uint64) ([]*thebegateway.TransactionRecord, error) {
	return nil, nil
}
func (h *roundTripHandle) GetL1FinalityForBlock(_ context.Context, _ uint64) (*thebegateway.L1FinalityRecord, error) {
	return nil, nil
}

func TestGetZKBlockByNumber_PreservesZKFieldsThroughJSON(t *testing.T) {
	stark := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	h := &roundTripHandle{
		blockRec: &thebegateway.BlockRecord{BlockNumber: 900},
		proofRec: &thebegateway.ZKProofRecord{BlockNumber: 900, ProofHash: "0xproofhash", StarkProof: stark},
	}
	SetGlobalHandle(h)
	defer SetGlobalHandle(nil)

	blk, err := GetZKBlockByNumber(nil, 900)
	if err != nil {
		t.Fatalf("GetZKBlockByNumber: %v", err)
	}
	if blk.ProofHash != "0xproofhash" {
		t.Errorf("ProofHash dropped in reconstruction: got %q", blk.ProofHash)
	}
	if !bytes.Equal(blk.StarkProof, stark) {
		t.Errorf("StarkProof dropped in reconstruction: got %x", blk.StarkProof)
	}

	// Provider path: json.Marshal then parse back — what thebesync ships on the wire.
	raw, err := json.Marshal(blk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var blk2 config.ZKBlock
	if err := json.Unmarshal(raw, &blk2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if blk2.ProofHash != "0xproofhash" {
		t.Errorf("ProofHash did not survive json round-trip: got %q", blk2.ProofHash)
	}
	if !bytes.Equal(blk2.StarkProof, stark) {
		t.Errorf("StarkProof did not survive json round-trip: got %x", blk2.StarkProof)
	}
}
