package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gossipnode/config"
	"gossipnode/messaging/BlockProcessing"

	"github.com/ethereum/go-ethereum/common"
)

// producedBlockHarness replaces the apply+store call and the halt with fakes.
// results[i] is what apply attempt i returns; past the end, the last repeats.
type producedBlockHarness struct {
	calls   int
	results []error
	halted  []string
}

func installProducedBlockHarness(t *testing.T, results ...error) *producedBlockHarness {
	t.Helper()
	h := &producedBlockHarness{results: results}
	prevApply, prevHalt := applyAndStoreBlock, haltBlockApply
	prevAttempts, prevDelay := keptBlockAttempts, keptBlockRetryDelay
	applyAndStoreBlock = func(context.Context, *config.ZKBlock) error {
		i := h.calls
		h.calls++
		if i >= len(h.results) {
			i = len(h.results) - 1
		}
		return h.results[i]
	}
	haltBlockApply = func(_ context.Context, reason string) { h.halted = append(h.halted, reason) }
	keptBlockAttempts, keptBlockRetryDelay = 3, time.Millisecond
	t.Cleanup(func() {
		applyAndStoreBlock, haltBlockApply = prevApply, prevHalt
		keptBlockAttempts, keptBlockRetryDelay = prevAttempts, prevDelay
	})
	return h
}

func keptErr() error {
	return fmt.Errorf("block 3: store failed after contract-state commit: %w: %w",
		BlockProcessing.ErrBlockKeptApplied, errors.New("connection reset"))
}

func producedBlock() *config.ZKBlock {
	return &config.ZKBlock{BlockNumber: 3, BlockHash: common.HexToHash("0x03")}
}

// A store that fails once: the producer applies the same certified block
// again, which completes it, and does not halt.
func TestApplyProducedBlock_KeptBlockCompletedOnRetry(t *testing.T) {
	h := installProducedBlockHarness(t, keptErr(), nil)
	if err := applyProducedBlock(context.Background(), producedBlock()); err != nil {
		t.Fatalf("applyProducedBlock: %v", err)
	}
	if h.calls != 2 {
		t.Fatalf("apply called %d times, want 2", h.calls)
	}
	if len(h.halted) != 0 {
		t.Fatalf("halted: %v", h.halted)
	}
}

// A store that keeps failing: the producer halts block apply, so the next
// round is not built on a block no peer has.
func TestApplyProducedBlock_KeptBlockHaltsWhenItCannotComplete(t *testing.T) {
	h := installProducedBlockHarness(t, keptErr())
	err := applyProducedBlock(context.Background(), producedBlock())
	if !errors.Is(err, BlockProcessing.ErrApplyHalted) {
		t.Fatalf("err = %v, want ErrApplyHalted", err)
	}
	if h.calls != 1+keptBlockAttempts {
		t.Fatalf("apply called %d times, want %d", h.calls, 1+keptBlockAttempts)
	}
	if len(h.halted) != 1 || !strings.Contains(h.halted[0], "block 3") {
		t.Fatalf("halt reasons = %v", h.halted)
	}
}

// After the first attempt kept the block, a later failure of any kind (the
// marked transactions are skipped, so it is no longer ErrBlockKeptApplied)
// still halts.
func TestApplyProducedBlock_KeptBlockThenOtherErrorHalts(t *testing.T) {
	h := installProducedBlockHarness(t, keptErr(), errors.New("block 3: store failed after apply, rolled back"))
	err := applyProducedBlock(context.Background(), producedBlock())
	if !errors.Is(err, BlockProcessing.ErrApplyHalted) || len(h.halted) != 1 {
		t.Fatalf("err = %v, halts = %v; want one halt", err, h.halted)
	}
}

// Any other failure is returned as before: the block was rolled back, nothing
// is retried and nothing halts.
func TestApplyProducedBlock_OtherErrorsUnchanged(t *testing.T) {
	plain := errors.New("block 3: store failed after apply, rolled back (fail closed)")
	h := installProducedBlockHarness(t, plain)
	if err := applyProducedBlock(context.Background(), producedBlock()); !errors.Is(err, plain) {
		t.Fatalf("err = %v, want the apply error", err)
	}
	if h.calls != 1 || len(h.halted) != 0 {
		t.Fatalf("calls=%d halts=%v; want 1 call, no halt", h.calls, h.halted)
	}
	h2 := installProducedBlockHarness(t, nil)
	if err := applyProducedBlock(context.Background(), producedBlock()); err != nil || h2.calls != 1 {
		t.Fatalf("success path: err=%v calls=%d", err, h2.calls)
	}
}
