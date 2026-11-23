package Sequencer

import (
	"context"
	"errors"
	"testing"
	"time"

	"gossipnode/config"
	"gossipnode/internal/pendingcommit"
	"gossipnode/messaging"
)

// A store outage longer than one retry series halts the height; the watcher
// keeps retrying and commits the same block once the store heals, without a
// restart.
func TestPendingCommitWatcherCommitsAfterOutageHeals(t *testing.T) {
	outage := errors.New("store unavailable")
	errs := make([]error, 0, 12)
	for range 10 { // more than one series: len(certifiedCommitDelays)+1 = 4
		errs = append(errs, outage)
	}
	f := &fakeCommit{tip: 69, applyErrs: errs}
	installFakeCommit(t, f)
	prevInterval := pendingRetryInterval
	pendingRetryInterval = time.Millisecond
	t.Cleanup(func() { pendingRetryInterval = prevInterval })

	block := certifiedTestBlock(70, 4)
	if err := commitCertifiedBlock(context.Background(), block, testCert); !errors.Is(err, ErrCertifiedBlockHalted) {
		t.Fatalf("setup: the live attempt must halt, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunPendingCommitWatcher(ctx, fakeHost{}); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rec, _ := pendingCommits.Current(); rec == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the watcher did not commit the block after the store healed")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	last := f.applied[len(f.applied)-1]
	if last.BlockHash != block.BlockHash || last.Period != 4 {
		t.Fatalf("the watcher must commit the same block at the same period, got %+v", last)
	}
	if f.recorded != 1 || f.broadcasted != 1 {
		t.Fatalf("want the certificate recorded once and the block broadcast once, got %d and %d", f.recorded, f.broadcasted)
	}
	if got := messaging.DefaultProposerPeriods.PeriodFor(70); got != 0 {
		t.Fatalf("the period must not advance, got %d", got)
	}
}

// The watcher returns when its context is cancelled.
func TestPendingCommitWatcherStopsOnCancel(t *testing.T) {
	installFakeCommit(t, &fakeCommit{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunPendingCommitWatcher(ctx, fakeHost{}); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not stop after cancel")
	}
}

// A record the live path has already committed and cleared is not committed
// or broadcast again by a resume.
func TestResumeAfterLiveCommitDoesNothing(t *testing.T) {
	f := &fakeCommit{tip: 79}
	installFakeCommit(t, f)
	block := certifiedTestBlock(80, 1)
	if err := commitCertifiedBlock(context.Background(), block, testCert); err != nil {
		t.Fatal(err)
	}
	if err := ResumePendingCommit(context.Background(), fakeHost{}); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 1 || f.broadcasted != 0 {
		t.Fatalf("want 1 apply (live) and no broadcast from the resume, got %d and %d", len(f.applied), f.broadcasted)
	}
}

// The halt alert is sent once per height per haltAlertInterval while the
// watcher keeps retrying; a new height alerts at once.
func TestHaltAlertPacing(t *testing.T) {
	var p haltAlertPacer
	t0 := time.Unix(1_000_000, 0)
	if !p.due(5, t0) {
		t.Fatal("the first halt must alert")
	}
	if p.due(5, t0.Add(time.Minute)) {
		t.Fatal("a repeat within the interval must not alert")
	}
	if !p.due(6, t0.Add(2*time.Minute)) {
		t.Fatal("a new height must alert")
	}
	if !p.due(6, t0.Add(2*time.Minute+haltAlertInterval)) {
		t.Fatal("the same height must alert again after the interval")
	}
}

// A dropped pending block's transactions are reported to the orchestrator as
// not landed, with the dropped block itself.
func TestReportDroppedPendingBlock(t *testing.T) {
	var got []*config.ZKBlock
	var reasons []string
	prev := sendRejectionReport
	sendRejectionReport = func(b *config.ZKBlock, reason, _ string) {
		got = append(got, b)
		reasons = append(reasons, reason)
	}
	t.Cleanup(func() { sendRejectionReport = prev })

	block := certifiedTestBlock(95, 2)
	rec, err := pendingRecordFor(block, testCert)
	if err != nil {
		t.Fatal(err)
	}
	ReportDroppedPendingBlock(rec)
	if len(got) != 1 || got[0].BlockNumber != 95 || got[0].BlockHash != block.BlockHash {
		t.Fatalf("want one report for the dropped block, got %+v", got)
	}
	if reasons[0] != droppedPendingReason {
		t.Fatalf("unexpected reason %q", reasons[0])
	}

	rec.Block = []byte("not json")
	ReportDroppedPendingBlock(rec)
	if len(got) != 1 {
		t.Fatal("an undecodable record must not be reported")
	}
}

// An operator drop during a retry series stops it: no further apply, nothing
// broadcast or recorded, the record stays gone and the dropped block is
// reported to the orchestrator once.
func TestDropDuringRetryStopsTheSeries(t *testing.T) {
	f := &fakeCommit{tip: 99, applyErrs: []error{errors.New("store timeout"), errors.New("store timeout")}}
	installFakeCommit(t, f)
	var reports int
	prev := sendRejectionReport
	sendRejectionReport = func(*config.ZKBlock, string, string) { reports++ }
	t.Cleanup(func() { sendRejectionReport = prev })

	block := certifiedTestBlock(100, 3)
	dropped := false
	certifiedCommitSleep = func(context.Context, time.Duration) bool {
		if !dropped {
			dropped = true
			if _, err := DropPendingCommit(100, block.BlockHash.Hex()); err != nil {
				t.Errorf("drop: %v", err)
			}
		}
		return true
	}

	err := commitCertifiedBlock(context.Background(), block, testCert)
	if !errors.Is(err, ErrCertifiedBlockDropped) {
		t.Fatalf("want ErrCertifiedBlockDropped, got %v", err)
	}
	if len(f.applied) != 1 {
		t.Fatalf("no apply may start after the drop, got %d applies", len(f.applied))
	}
	if f.recorded != 0 || f.broadcasted != 0 {
		t.Fatalf("a dropped block must not be recorded or broadcast, got %d and %d", f.recorded, f.broadcasted)
	}
	if rec, _ := pendingCommits.Current(); rec != nil {
		t.Fatalf("the record must stay dropped, got %+v", rec)
	}
	if reports != 1 {
		t.Fatalf("want one orchestrator report for the dropped block, got %d", reports)
	}
}

// A resume that read the record before an operator dropped it does not
// re-create the record or apply the block.
func TestResumeAfterDropDoesNotRecreateRecord(t *testing.T) {
	f := &fakeCommit{tip: 109}
	installFakeCommit(t, f)
	prev := sendRejectionReport
	sendRejectionReport = func(*config.ZKBlock, string, string) {}
	t.Cleanup(func() { sendRejectionReport = prev })

	block := certifiedTestBlock(110, 1)
	rec, err := pendingRecordFor(block, testCert)
	if err != nil {
		t.Fatal(err)
	}
	if err := pendingCommits.Save(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := DropPendingCommit(110, block.BlockHash.Hex()); err != nil {
		t.Fatal(err)
	}
	// What the resume does after it has read the record.
	certifiedCommitMu.Lock()
	err = commitCertifiedBlockLocked(context.Background(), block, testCert)
	certifiedCommitMu.Unlock()
	if !errors.Is(err, ErrCertifiedBlockDropped) {
		t.Fatalf("want ErrCertifiedBlockDropped, got %v", err)
	}
	if len(f.applied) != 0 {
		t.Fatalf("a dropped block must not be applied, got %d", len(f.applied))
	}
	if got, _ := pendingCommits.Current(); got != nil {
		t.Fatalf("the resume path must not re-create the record, got %+v", got)
	}
}

// DropPendingCommit refuses a wrong height or hash and an empty tracker.
func TestDropPendingCommitRefusesMismatch(t *testing.T) {
	installFakeCommit(t, &fakeCommit{})
	if _, err := DropPendingCommit(1, "0x1"); !errors.Is(err, pendingcommit.ErrNoPending) {
		t.Fatalf("want ErrNoPending, got %v", err)
	}
	block := certifiedTestBlock(120, 1)
	rec, _ := pendingRecordFor(block, testCert)
	_ = pendingCommits.Save(rec)
	if _, err := DropPendingCommit(121, block.BlockHash.Hex()); !errors.Is(err, pendingcommit.ErrRecordMismatch) {
		t.Fatalf("want ErrRecordMismatch, got %v", err)
	}
	if got, _ := pendingCommits.Current(); got == nil {
		t.Fatal("a refused drop must keep the record")
	}
}
