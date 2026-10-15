package DB_OPs

import (
	"context"
	"testing"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
)

// headerOnlyHandle fails the test if anything beyond the blocks row is read.
type headerOnlyHandle struct {
	store.ThebeHandle
	t   *testing.T
	rec *thebegateway.BlockRecord
}

func (h *headerOnlyHandle) GetBlock(_ context.Context, _ uint64) (*thebegateway.BlockRecord, error) {
	return h.rec, nil
}
func (h *headerOnlyHandle) GetZKProof(_ context.Context, _ uint64) (*thebegateway.ZKProofRecord, error) {
	h.t.Fatal("GetZKBlockHeaderByNumber must not read the ZK proof")
	return nil, nil
}
func (h *headerOnlyHandle) GetTransactionsByBlock(_ context.Context, _ uint64) ([]*thebegateway.TransactionRecord, error) {
	h.t.Fatal("GetZKBlockHeaderByNumber must not read transactions")
	return nil, nil
}

// The header read returns the blocks-row fields the switched callers use
// (BlockHash, StateRoot, CommitteeCertificate, GasLimit, Coinbase) and never
// touches the proof or transactions.
func TestGetZKBlockHeaderByNumber_HeaderFieldsOnly(t *testing.T) {
	const hash = "0x1111111111111111111111111111111111111111111111111111111111111111"
	const root = "0x2222222222222222222222222222222222222222222222222222222222222222"
	h := &headerOnlyHandle{t: t, rec: &thebegateway.BlockRecord{
		BlockNumber:  42,
		BlockHash:    hash,
		StateRoot:    root,
		GasLimit:     30_000_000,
		CoinbaseAddr: "0x3333333333333333333333333333333333333333",
		ExtraData:    map[string]any{"committee_certificate": `[{"peer_id":"p","agree":true}]`},
	}}
	SetGlobalHandle(h)
	defer SetGlobalHandle(nil)

	blk, err := GetZKBlockHeaderByNumber(42)
	if err != nil {
		t.Fatalf("GetZKBlockHeaderByNumber: %v", err)
	}
	if blk.BlockNumber != 42 || blk.BlockHash.Hex() != hash || blk.StateRoot.Hex() != root || blk.GasLimit != 30_000_000 {
		t.Fatalf("header fields wrong: %+v", blk)
	}
	if blk.CommitteeCertificate == "" {
		t.Fatal("CommitteeCertificate must be hydrated from ExtraData (PrevBlockCertSigners depends on it)")
	}
	if blk.CoinbaseAddr == nil {
		t.Fatal("CoinbaseAddr must be populated")
	}
	if blk.StarkProof != nil || blk.ProofHash != "" || len(blk.Transactions) != 0 {
		t.Fatal("header read must not carry proof or transactions")
	}
}
