package Sequencer

import (
	"context"
	"errors"
	"testing"
	"time"

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
