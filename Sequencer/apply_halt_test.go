package Sequencer

import (
	"errors"
	"testing"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/store"
	"gossipnode/config"
	"gossipnode/messaging/BlockProcessing"
)

// haltRecordStore holds a recorded block-apply halt and nothing else.
type haltRecordStore struct {
	store.ThebeHandle
	reason string
}

func (h *haltRecordStore) GetSyncKV(string) ([]byte, error) { return []byte(h.reason), nil }
func (h *haltRecordStore) PutSyncKV(_ string, v []byte) error {
	h.reason = string(v)
	return nil
}

// A node whose block apply is halted does not start a consensus round.
func TestStart_RefusesWhileApplyHalted(t *testing.T) {
	DB_OPs.SetGlobalHandle(&haltRecordStore{reason: "block 8 failed after committing contract state"})
	t.Cleanup(func() { DB_OPs.SetGlobalHandle(nil) })
	// The halt is process-wide: lift it so no later test in this package is
	// refused (cleanups run last-in first-out, so the store is still set).
	t.Cleanup(func() {
		if _, err := BlockProcessing.ClearApplyHalt(); err != nil {
			t.Errorf("clear the halt: %v", err)
		}
	})

	err := (&Consensus{}).Start(&config.ZKBlock{BlockNumber: 9})
	if !errors.Is(err, BlockProcessing.ErrApplyHalted) {
		t.Fatalf("want ErrApplyHalted before any round starts, got %v", err)
	}
}
