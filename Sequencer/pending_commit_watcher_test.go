package Sequencer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/internal/pendingcommit"
	"gossipnode/internal/proposalguard"
	"gossipnode/messaging"

	"github.com/ethereum/go-ethereum/common"
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

// The live path does not re-save a block an operator already dropped: the
// record stays gone and proposals at the height are no longer refused.
func TestLiveCommitAfterDropDoesNotRecreateRecord(t *testing.T) {
	f := &fakeCommit{tip: 129}
	installFakeCommit(t, f)
	prev := sendRejectionReport
	sendRejectionReport = func(*config.ZKBlock, string, string) {}
	t.Cleanup(func() { sendRejectionReport = prev })

	block := certifiedTestBlock(130, 2)
	rec, err := pendingRecordFor(block, testCert)
	if err != nil {
		t.Fatal(err)
	}
	if err := pendingCommits.Save(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := DropPendingCommit(130, block.BlockHash.Hex()); err != nil {
		t.Fatal(err)
	}

	if err := commitCertifiedBlock(context.Background(), block, testCert); !errors.Is(err, ErrCertifiedBlockDropped) {
		t.Fatalf("want ErrCertifiedBlockDropped, got %v", err)
	}
	if got, _ := pendingCommits.Current(); got != nil {
		t.Fatalf("the live path must not re-create a dropped record, got %+v", got)
	}
	if blocked, _, _ := pendingCommits.BlocksProposal(130, 129); blocked {
		t.Fatal("proposals at the height must not be refused after the drop")
	}
	if len(f.applied) != 0 || f.broadcasted != 0 {
		t.Fatalf("a dropped block must not be applied or broadcast, got %d and %d", len(f.applied), f.broadcasted)
	}
}

// A record re-created for a dropped block (for example by a race) is removed
// when an attempt sees the drop.
func TestDroppedAttemptRemovesRecreatedRecord(t *testing.T) {
	f := &fakeCommit{tip: 139}
	installFakeCommit(t, f)
	prev := sendRejectionReport
	sendRejectionReport = func(*config.ZKBlock, string, string) {}
	t.Cleanup(func() { sendRejectionReport = prev })

	block := certifiedTestBlock(140, 1)
	rec, _ := pendingRecordFor(block, testCert)
	_ = pendingCommits.Save(rec)
	if _, err := DropPendingCommit(140, block.BlockHash.Hex()); err != nil {
		t.Fatal(err)
	}
	_ = pendingCommits.Save(rec) // re-created behind the drop's back

	certifiedCommitMu.Lock()
	err := commitCertifiedBlockLocked(context.Background(), block, testCert)
	certifiedCommitMu.Unlock()
	if !errors.Is(err, ErrCertifiedBlockDropped) {
		t.Fatalf("want ErrCertifiedBlockDropped, got %v", err)
	}
	if got, _ := pendingCommits.Current(); got != nil {
		t.Fatalf("the re-created record must be removed, got %+v", got)
	}
}

// DropUnreadablePendingCommit refuses a readable record.
func TestDropUnreadablePendingCommitRefusesReadable(t *testing.T) {
	installFakeCommit(t, &fakeCommit{})
	if err := DropUnreadablePendingCommit(); !errors.Is(err, pendingcommit.ErrNoPending) {
		t.Fatalf("want ErrNoPending, got %v", err)
	}
	block := certifiedTestBlock(150, 1)
	rec, _ := pendingRecordFor(block, testCert)
	_ = pendingCommits.Save(rec)
	if err := DropUnreadablePendingCommit(); !errors.Is(err, pendingcommit.ErrReadable) {
		t.Fatalf("want ErrReadable, got %v", err)
	}
	if got, _ := pendingCommits.Current(); got == nil {
		t.Fatal("a readable record must be kept")
	}
}

// After a drop, the same transactions certified again at a later period (same
// BlockHash, different ConsensusHash) commit normally.
func TestReproposalAfterDropCommits(t *testing.T) {
	f := &fakeCommit{tip: 159}
	installFakeCommit(t, f)
	prev := sendRejectionReport
	sendRejectionReport = func(*config.ZKBlock, string, string) {}
	t.Cleanup(func() { sendRejectionReport = prev })

	dropped := certifiedTestBlock(160, 1)
	dropped.ConsensusHash = common.HexToHash("0xc1")
	rec, err := pendingRecordFor(dropped, testCert)
	if err != nil {
		t.Fatal(err)
	}
	if err := pendingCommits.Save(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := DropPendingCommit(160, dropped.BlockHash.Hex()); err != nil {
		t.Fatal(err)
	}

	again := certifiedTestBlock(160, 2) // same transactions, so the same BlockHash
	again.ConsensusHash = common.HexToHash("0xc2")
	if again.BlockHash != dropped.BlockHash {
		t.Fatal("setup: the re-proposal must share the BlockHash")
	}
	if err := commitCertifiedBlock(context.Background(), again, testCert); err != nil {
		t.Fatalf("the re-proposed round must commit, got %v", err)
	}
	if len(f.applied) != 1 || f.applied[0].Period != 2 {
		t.Fatalf("want the period-2 block applied once, got %+v", f.applied)
	}
	if got, _ := pendingCommits.Current(); got != nil {
		t.Fatalf("the record must be cleared after the commit, got %+v", got)
	}
}

// A stale attempt for the dropped round does not remove the record of a new
// round of the same transactions at that height.
func TestDroppedAttemptKeepsNewRoundRecord(t *testing.T) {
	f := &fakeCommit{tip: 169}
	installFakeCommit(t, f)
	prev := sendRejectionReport
	sendRejectionReport = func(*config.ZKBlock, string, string) {}
	t.Cleanup(func() { sendRejectionReport = prev })

	dropped := certifiedTestBlock(170, 1)
	dropped.ConsensusHash = common.HexToHash("0xd1")
	rec, _ := pendingRecordFor(dropped, testCert)
	_ = pendingCommits.Save(rec)
	if _, err := DropPendingCommit(170, dropped.BlockHash.Hex()); err != nil {
		t.Fatal(err)
	}
	again := certifiedTestBlock(170, 2)
	again.ConsensusHash = common.HexToHash("0xd2")
	recAgain, _ := pendingRecordFor(again, testCert)
	if err := pendingCommits.Save(recAgain); err != nil {
		t.Fatal(err)
	}

	certifiedCommitMu.Lock()
	err := commitCertifiedBlockLocked(context.Background(), dropped, testCert)
	certifiedCommitMu.Unlock()
	if !errors.Is(err, ErrCertifiedBlockDropped) {
		t.Fatalf("want ErrCertifiedBlockDropped, got %v", err)
	}
	got, _ := pendingCommits.Current()
	if got == nil || got.RoundID != certifiedRoundID(again) {
		t.Fatalf("the new round's record must be kept, got %+v", got)
	}
}

// A drop moves the height to a period above the dropped block's and releases
// the height's proposal claim, so the same transactions proposed again form a
// new round that commits.
func TestDropStartsNewRound(t *testing.T) {
	f := &fakeCommit{tip: 179}
	installFakeCommit(t, f)
	prevReport := sendRejectionReport
	sendRejectionReport = func(*config.ZKBlock, string, string) {}
	t.Cleanup(func() { sendRejectionReport = prevReport })
	var released []uint64
	prevRelease := releaseProposalClaim
	releaseProposalClaim = func(h uint64) { released = append(released, h) }
	t.Cleanup(func() { releaseProposalClaim = prevRelease })

	const h = 180
	for messaging.DefaultProposerPeriods.PeriodFor(h) < 3 { // the dropped round ran at period 3
		messaging.DefaultProposerPeriods.AdvanceAfterFailure(h, 7)
	}
	dropped := certifiedTestBlock(h, messaging.DefaultProposerPeriods.PeriodFor(h))
	rec, _ := pendingRecordFor(dropped, testCert)
	_ = pendingCommits.Save(rec)
	if _, err := DropPendingCommit(h, dropped.BlockHash.Hex()); err != nil {
		t.Fatal(err)
	}

	next := messaging.DefaultProposerPeriods.PeriodFor(h)
	if next <= dropped.Period {
		t.Fatalf("the drop must move the height above period %d, got %d", dropped.Period, next)
	}
	if len(released) != 1 || released[0] != h {
		t.Fatalf("the drop must release the height's proposal claim, got %v", released)
	}
	// The same transactions proposed again, at the period the proposer now uses.
	again := certifiedTestBlock(h, next)
	if err := commitCertifiedBlock(context.Background(), again, testCert); err != nil {
		t.Fatalf("the next round must commit, got %v", err)
	}
	if len(f.applied) != 1 || f.applied[0].Period != next {
		t.Fatalf("want the new round applied once, got %+v", f.applied)
	}
}

// When the live series stops on a drop, it does not release the proposal
// claim again: a new round admitted after the drop keeps its claim.
func TestLiveStopOnDropKeepsNewRoundClaim(t *testing.T) {
	f := &fakeCommit{tip: 189, applyErrs: []error{errors.New("store timeout"), errors.New("store timeout")}}
	installFakeCommit(t, f)
	prevReport := sendRejectionReport
	sendRejectionReport = func(*config.ZKBlock, string, string) {}
	t.Cleanup(func() { sendRejectionReport = prevReport })
	prevGuard := proposalguard.Default
	proposalguard.Default = proposalguard.New(proposalguard.DefaultTTL)
	prevRelease := releaseProposalClaim
	releaseProposalClaim = proposalguard.Default.Release
	t.Cleanup(func() { proposalguard.Default, releaseProposalClaim = prevGuard, prevRelease })

	const h = 190
	block := certifiedTestBlock(h, 1)
	rec, _ := pendingRecordFor(block, testCert)
	_ = pendingCommits.Save(rec)
	dropped := false
	certifiedCommitSleep = func(context.Context, time.Duration) bool {
		if !dropped {
			dropped = true
			if _, err := DropPendingCommit(h, block.BlockHash.Hex()); err != nil {
				t.Errorf("drop: %v", err)
			}
			if !proposalguard.Default.Claim(h) { // the new round's proposal
				t.Error("setup: the new round must be able to claim the height after the drop")
			}
		}
		return true
	}

	c := &Consensus{ZKBlockData: PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(block), mu: &sync.RWMutex{}}
	if err := c.BroadcastAndProcessBlock(context.Background(), testCert, true); !errors.Is(err, ErrCertifiedBlockDropped) {
		t.Fatalf("want ErrCertifiedBlockDropped, got %v", err)
	}
	if !proposalguard.Default.InFlight(h) {
		t.Fatal("the new round's claim must survive the stopped live series")
	}
}

// stateByHeight makes storedBlockStateAt return a per-height state.
func stateByHeight(states map[uint64]storedState, errAt map[uint64]error) {
	storedBlockStateAt = func(b *config.ZKBlock) (storedState, error) {
		if err := errAt[b.BlockNumber]; err != nil {
			return storedNone, err
		}
		return states[b.BlockNumber], nil
	}
}

// captureReports replaces the orchestrator report and returns the reported
// blocks.
func captureReports(t *testing.T) *[]*config.ZKBlock {
	t.Helper()
	var got []*config.ZKBlock
	prev := sendRejectionReport
	sendRejectionReport = func(b *config.ZKBlock, _, _ string) { got = append(got, b) }
	t.Cleanup(func() { sendRejectionReport = prev })
	return &got
}

// A pending record at a height where a different block is stored (the chain
// moved on without it) is reported to the orchestrator and removed when the
// next height is recorded.
func TestSupersededRecordIsReportedAndCleared(t *testing.T) {
	f := &fakeCommit{tip: 200}
	installFakeCommit(t, f)
	reports := captureReports(t)
	old := certifiedTestBlock(200, 1)
	rec, _ := pendingRecordFor(old, testCert)
	_ = pendingCommits.Save(rec)
	stateByHeight(map[uint64]storedState{200: storedDifferent}, nil)

	next := certifiedTestBlock(201, 1)
	_ = recordPendingCertified(context.Background(), next, testCert)
	if len(*reports) != 1 || (*reports)[0].BlockNumber != 200 {
		t.Fatalf("want the superseded block reported once, got %+v", *reports)
	}
	got, _ := pendingCommits.Current()
	if got == nil || got.Height != 201 {
		t.Fatalf("height 201 must now be recorded, got %+v", got)
	}
}

// A record whose block is stored and applied is removed without a report.
func TestCommittedRecordBelowTipIsClearedSilently(t *testing.T) {
	installFakeCommit(t, &fakeCommit{tip: 210})
	reports := captureReports(t)
	rec, _ := pendingRecordFor(certifiedTestBlock(210, 1), testCert)
	_ = pendingCommits.Save(rec)
	stateByHeight(map[uint64]storedState{210: storedSameApplied}, nil)

	_ = recordPendingCertified(context.Background(), certifiedTestBlock(211, 1), testCert)
	if len(*reports) != 0 {
		t.Fatalf("a committed block must not be reported, got %d", len(*reports))
	}
	if got, _ := pendingCommits.Current(); got == nil || got.Height != 211 {
		t.Fatalf("height 211 must now be recorded, got %+v", got)
	}
}

// When the stored state at the record's height cannot be read, the record is
// kept (fail closed) and nothing is reported.
func TestRecordBelowTipKeptWhenStateUnknown(t *testing.T) {
	installFakeCommit(t, &fakeCommit{tip: 220})
	reports := captureReports(t)
	rec, _ := pendingRecordFor(certifiedTestBlock(220, 1), testCert)
	_ = pendingCommits.Save(rec)
	stateByHeight(nil, map[uint64]error{220: errors.New("read failed")})

	_ = recordPendingCertified(context.Background(), certifiedTestBlock(221, 1), testCert)
	if len(*reports) != 0 {
		t.Fatalf("nothing may be reported, got %d", len(*reports))
	}
	if got, _ := pendingCommits.Current(); got == nil || got.Height != 220 {
		t.Fatalf("the unsettled record must be kept, got %+v", got)
	}
}

// The watcher settles a superseded record instead of retrying it: reported
// once, removed, never applied.
func TestResumeSettlesSupersededRecord(t *testing.T) {
	f := &fakeCommit{tip: 231}
	installFakeCommit(t, f)
	reports := captureReports(t)
	rec, _ := pendingRecordFor(certifiedTestBlock(230, 1), testCert)
	_ = pendingCommits.Save(rec)
	stateByHeight(map[uint64]storedState{230: storedDifferent}, nil)

	if err := ResumePendingCommit(context.Background(), fakeHost{}); err != nil {
		t.Fatal(err)
	}
	if len(*reports) != 1 || len(f.applied) != 0 || f.broadcasted != 0 {
		t.Fatalf("want one report and no apply or broadcast, got %d, %d, %d", len(*reports), len(f.applied), f.broadcasted)
	}
	if got, _ := pendingCommits.Current(); got != nil {
		t.Fatalf("the superseded record must be removed, got %+v", got)
	}
}

// If the watcher commits a block from the record saved at quorum before the
// live path gets to it, the live path does nothing more: one apply, one
// certificate, one broadcast, and no record left behind.
func TestLivePathSkipsBlockTheWatcherCommitted(t *testing.T) {
	f := &fakeCommit{tip: 239}
	installFakeCommit(t, f)
	const h = 240
	block := certifiedTestBlock(h, 1)
	rec, _ := pendingRecordFor(block, testCert) // saved at quorum
	_ = pendingCommits.Save(rec)

	if err := ResumePendingCommit(context.Background(), fakeHost{}); err != nil { // the watcher tick
		t.Fatal(err)
	}
	// The block is now stored and applied, and the tip covers it.
	f.tip = h
	stateByHeight(map[uint64]storedState{h: storedSameApplied}, nil)

	c := &Consensus{ZKBlockData: PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(block), mu: &sync.RWMutex{}}
	if err := c.BroadcastAndProcessBlock(context.Background(), testCert, true); err != nil {
		t.Fatalf("the live path must treat a committed block as done, got %v", err)
	}
	if len(f.applied) != 1 || f.recorded != 1 || f.broadcasted != 1 {
		t.Fatalf("want one apply, one certificate and one broadcast, got %d, %d, %d", len(f.applied), f.recorded, f.broadcasted)
	}
	if got, _ := pendingCommits.Current(); got != nil {
		t.Fatalf("no record may be left behind, got %+v", got)
	}
}

// A drop raises the height above the dropped period directly, in one step,
// even on a fresh (restarted) period counter.
func TestDropRaisesAboveHighPeriod(t *testing.T) {
	installFakeCommit(t, &fakeCommit{tip: 249})
	prev := sendRejectionReport
	sendRejectionReport = func(*config.ZKBlock, string, string) {}
	t.Cleanup(func() { sendRejectionReport = prev })
	prevRelease := releaseProposalClaim
	releaseProposalClaim = func(uint64) {}
	t.Cleanup(func() { releaseProposalClaim = prevRelease })

	dropped := certifiedTestBlock(250, 1000)
	rec, _ := pendingRecordFor(dropped, testCert)
	_ = pendingCommits.Save(rec)
	if _, err := DropPendingCommit(250, dropped.BlockHash.Hex()); err != nil {
		t.Fatal(err)
	}
	if got := messaging.DefaultProposerPeriods.PeriodFor(250); got <= 1000 {
		t.Fatalf("the height must move above period 1000, got %d", got)
	}
}
