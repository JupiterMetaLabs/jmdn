package Sequencer

import (
	"errors"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// A certified block that fails to apply or store on the sequencer is withheld,
// so the orchestrator must be told its transactions did not land.
func TestReportLocalApplyFailure(t *testing.T) {
	var gotBlock *config.ZKBlock
	var gotReason, gotDetail string
	calls := 0
	prev := sendRejectionReport
	sendRejectionReport = func(b *config.ZKBlock, reason, detail string) {
		calls++
		gotBlock, gotReason, gotDetail = b, reason, detail
	}
	t.Cleanup(func() { sendRejectionReport = prev })

	block := &config.ZKBlock{BlockNumber: 21829, BlockHash: common.HexToHash("0xf4a9")}
	reportLocalApplyFailure(block, errors.New("failed to store block: duplicate key"))

	if calls != 1 {
		t.Fatalf("report sent %d times, want 1", calls)
	}
	if gotBlock != block {
		t.Fatal("report carries a different block")
	}
	if gotReason != localApplyFailureReason {
		t.Fatalf("reason = %q", gotReason)
	}
	if gotDetail != "failed to store block: duplicate key" {
		t.Fatalf("detail = %q", gotDetail)
	}

	reportLocalApplyFailure(block, nil)
	if calls != 2 || gotDetail != "" {
		t.Fatalf("nil error: calls=%d detail=%q", calls, gotDetail)
	}
}
