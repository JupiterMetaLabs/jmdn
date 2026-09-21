package DB_OPs

import (
	"context"
	"fmt"
	"testing"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// reprojectSpyHandle: blocks in `present` have a `blocks` row; others read as
// "not found". Records every StoreZKBlock (the snapshot-ensuring chain) call.
type reprojectSpyHandle struct {
	store.ThebeHandle
	present map[uint64]bool
	stored  []uint64
}

func (h *reprojectSpyHandle) GetBlock(_ context.Context, n uint64) (*thebegateway.BlockRecord, error) {
	if h.present[n] {
		return &thebegateway.BlockRecord{BlockNumber: n}, nil
	}
	return nil, fmt.Errorf("no rows in result set")
}
func (h *reprojectSpyHandle) GetZKProof(_ context.Context, _ uint64) (*thebegateway.ZKProofRecord, error) {
	return nil, fmt.Errorf("no rows in result set")
}
func (h *reprojectSpyHandle) GetTransactionsByBlock(_ context.Context, _ uint64) ([]*thebegateway.TransactionRecord, error) {
	return nil, nil
}
func (h *reprojectSpyHandle) GetL1FinalityForBlock(_ context.Context, _ uint64) (*thebegateway.L1FinalityRecord, error) {
	return nil, nil
}
func (h *reprojectSpyHandle) StoreZKBlock(_ context.Context, b *config.ZKBlock) error {
	h.stored = append(h.stored, b.BlockNumber)
	return nil
}

func TestReprojectRange_ReStoresEachPresentBlock(t *testing.T) {
	// The incident range: 822 and 827–828 present; 823–826 absent locally.
	h := &reprojectSpyHandle{present: map[uint64]bool{822: true, 827: true, 828: true}}
	SetGlobalHandle(h)
	defer SetGlobalHandle(nil)

	n, err := ReprojectRange(822, 828)
	if err != nil {
		t.Fatalf("ReprojectRange: %v", err)
	}
	if n != 3 {
		t.Errorf("reprojected = %d, want 3 (only the present blocks)", n)
	}
	if len(h.stored) != 3 {
		t.Errorf("StoreZKBlock (snapshot-ensure) calls = %d, want 3 — one per present block", len(h.stored))
	}
	for _, got := range h.stored {
		if got != 822 && got != 827 && got != 828 {
			t.Errorf("re-stored an absent block %d (should have been skipped)", got)
		}
	}
}

func TestReprojectRange_RejectsInvertedRange(t *testing.T) {
	if _, err := ReprojectRange(10, 5); err == nil {
		t.Error("expected an error for to < from")
	}
}
