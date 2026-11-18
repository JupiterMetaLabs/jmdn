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

// A node whose block apply is halted does not start a consensus round.
func TestStart_RefusesWhileApplyHalted(t *testing.T) {
	DB_OPs.SetGlobalHandle(&haltRecordStore{reason: "block 8 failed after committing contract state"})
	t.Cleanup(func() { DB_OPs.SetGlobalHandle(nil) })

	err := (&Consensus{}).Start(&config.ZKBlock{BlockNumber: 9})
	if !errors.Is(err, BlockProcessing.ErrApplyHalted) {
		t.Fatalf("want ErrApplyHalted before any round starts, got %v", err)
	}
}
