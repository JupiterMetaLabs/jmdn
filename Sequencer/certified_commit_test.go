package Sequencer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/internal/pendingcommit"
	"gossipnode/messaging"
	"gossipnode/messaging/BlockProcessing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
)

// fakeCommit replaces the hooks commitCertifiedBlock uses and records calls.
type fakeCommit struct {
	applyErrs   []error // error for each apply attempt; nil once exhausted
	applied     []*config.ZKBlock
	stored      storedState
	storedErr   error
	recorded    int
	broadcasted int
	tip         uint64
}

func installFakeCommit(t *testing.T, f *fakeCommit) {
	t.Helper()
	savedApply, savedRecord, savedStored := applyCertifiedBlock, recordCommitCert, storedBlockStateAt
	savedTip, savedBroadcast, savedPending := committedTip, broadcastCommitted, pendingCommits
	savedDelays, savedSleep := certifiedCommitDelays, certifiedCommitSleep
	savedPeriods := messaging.DefaultProposerPeriods
	t.Cleanup(func() {
		applyCertifiedBlock, recordCommitCert, storedBlockStateAt = savedApply, savedRecord, savedStored
		committedTip, broadcastCommitted, pendingCommits = savedTip, savedBroadcast, savedPending
		certifiedCommitDelays, certifiedCommitSleep = savedDelays, savedSleep
		messaging.DefaultProposerPeriods = savedPeriods
	})

	messaging.DefaultProposerPeriods = messaging.NewProposerPeriods()
	pendingCommits = pendingcommit.New()
	certifiedCommitDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	certifiedCommitSleep = func(context.Context, time.Duration) bool { return true }
	applyCertifiedBlock = func(b *config.ZKBlock, _ []BLS_Signer.BLSresponse) error {
		i := len(f.applied)
		f.applied = append(f.applied, b)
		if i < len(f.applyErrs) {
			return f.applyErrs[i]
		}
		return nil
	}
	recordCommitCert = func(uint64, []BLS_Signer.BLSresponse) { f.recorded++ }
	storedBlockStateAt = func(*config.ZKBlock) (storedState, error) { return f.stored, f.storedErr }
	committedTip = func() (uint64, error) { return f.tip, nil }
	broadcastCommitted = func(host.Host, *config.ZKBlock, []BLS_Signer.BLSresponse) error {
		f.broadcasted++
		return nil
	}
}

func certifiedTestBlock(height, period uint64) *config.ZKBlock {
	return &config.ZKBlock{
		BlockNumber: height,
		Period:      period,
		BlockHash:   common.HexToHash("0xabc1"),
		Status:      "verified",
	}
}

var testCert = []BLS_Signer.BLSresponse{{PeerID: "p1", Agree: true, Signature: "s1"}}

// A temporary write failure retries the SAME block (same hash, same period)
// and the certificate is recorded only after the block is stored.
func TestCertifiedBlockRetriesSameBlockAfterWriteFailure(t *testing.T) {
	f := &fakeCommit{applyErrs: []error{errors.New("immudb ExecAll timeout")}}
	installFakeCommit(t, f)
	block := certifiedTestBlock(50, 2)

	if err := commitCertifiedBlock(context.Background(), block, testCert); err != nil {
		t.Fatalf("a temporary failure must be retried, got %v", err)
	}
	if len(f.applied) != 2 {
		t.Fatalf("want 2 apply attempts (fail, then success), got %d", len(f.applied))
	}
	for i, b := range f.applied {
		if b.BlockHash != block.BlockHash || b.Period != 2 || b.BlockNumber != 50 {
			t.Fatalf("attempt %d applied a different block or period: %+v", i, b)
		}
	}
	if f.recorded != 1 {
		t.Fatalf("certificate must be recorded exactly once, after the commit; got %d", f.recorded)
	}
	if rec, _ := pendingCommits.Current(); rec != nil {
		t.Fatalf("pending record must be cleared after the commit, got %+v", rec)
	}
	if got := messaging.DefaultProposerPeriods.PeriodFor(50); got != 0 {
		t.Fatalf("the period must not advance while committing a certified block, got %d", got)
	}
}

// A failure that keeps repeating halts the height: no period advance, record
// kept (so proposals stay refused), certificate never recorded.
func TestCertifiedBlockRepeatingErrorHaltsHeight(t *testing.T) {
	dup := errors.New("uq_txn_block_index (23505)")
	f := &fakeCommit{applyErrs: []error{dup, dup, dup, dup, dup, dup}}
	installFakeCommit(t, f)
	block := certifiedTestBlock(51, 0)

	err := commitCertifiedBlock(context.Background(), block, testCert)
	if !errors.Is(err, ErrCertifiedBlockHalted) {
		t.Fatalf("want ErrCertifiedBlockHalted, got %v", err)
	}
	if want := len(certifiedCommitDelays) + 1; len(f.applied) != want {
		t.Fatalf("want %d attempts before halting, got %d", want, len(f.applied))
	}
	if f.recorded != 0 {
		t.Fatal("a certificate for an uncommitted block must not be recorded")
	}
	rec, _ := pendingCommits.Current()
	if rec == nil || rec.Height != 51 || rec.BlockHash != block.BlockHash.Hex() {
		t.Fatalf("pending record must be kept after a halt, got %+v", rec)
	}
	if blocked, _, _ := pendingCommits.BlocksProposal(51, 50); !blocked {
		t.Fatal("a new proposal at the halted height must be refused")
	}
	if got := messaging.DefaultProposerPeriods.PeriodFor(51); got != 0 {
		t.Fatalf("a halt must not advance the period, got %d", got)
	}
}

// When block apply on this node is halted (contract state that cannot be
// rolled back), retrying cannot help: halt after the first attempt, keep the
// record and do not advance the period.
func TestCertifiedBlockApplyHaltedHaltsAtOnce(t *testing.T) {
	halted := fmt.Errorf("apply block 52: %w (contract storage changed)", BlockProcessing.ErrApplyHalted)
	f := &fakeCommit{applyErrs: []error{halted, halted, halted}}
	installFakeCommit(t, f)
	block := certifiedTestBlock(52, 1)

	err := commitCertifiedBlock(context.Background(), block, testCert)
	if !errors.Is(err, ErrCertifiedBlockHalted) {
		t.Fatalf("want ErrCertifiedBlockHalted, got %v", err)
	}
	if len(f.applied) != 1 {
		t.Fatalf("want 1 apply attempt, got %d", len(f.applied))
	}
	if rec, _ := pendingCommits.Current(); rec == nil || rec.Height != 52 {
		t.Fatalf("pending record must be kept, got %+v", rec)
	}
	if got := messaging.DefaultProposerPeriods.PeriodFor(52); got != 0 {
		t.Fatalf("a halt must not advance the period, got %d", got)
	}
}

// A different block already stored at the height halts at once, without
// applying anything.
func TestCertifiedBlockDifferentStoredBlockHalts(t *testing.T) {
	f := &fakeCommit{stored: storedDifferent}
	installFakeCommit(t, f)
	err := commitCertifiedBlock(context.Background(), certifiedTestBlock(52, 0), testCert)
	if !errors.Is(err, ErrCertifiedBlockHalted) || len(f.applied) != 0 {
		t.Fatalf("want an immediate halt with no apply, got err=%v applies=%d", err, len(f.applied))
	}
}

// The block landed but only some of its transactions are applied: halt, never
// skip and never re-apply blindly.
func TestCertifiedBlockStoredPartlyAppliedHalts(t *testing.T) {
	f := &fakeCommit{stored: storedSamePartial}
	installFakeCommit(t, f)
	err := commitCertifiedBlock(context.Background(), certifiedTestBlock(53, 0), testCert)
	if !errors.Is(err, ErrCertifiedBlockHalted) || len(f.applied) != 0 {
		t.Fatalf("want an immediate halt with no apply, got err=%v applies=%d", err, len(f.applied))
	}
}

// The store reported an error, the apply was rolled back, and the queued write
// landed later: the block is stored with none of its transactions applied. It
// is applied again and committed, not halted.
func TestCertifiedBlockStoredButUnappliedIsAppliedAgain(t *testing.T) {
	f := &fakeCommit{stored: storedSameUnapplied}
	installFakeCommit(t, f)
	if err := commitCertifiedBlock(context.Background(), certifiedTestBlock(53, 0), testCert); err != nil {
		t.Fatalf("want the block applied again and committed, got %v", err)
	}
	if len(f.applied) != 1 || f.recorded != 1 {
		t.Fatalf("want one apply and one certificate record, got applies=%d recorded=%d", len(f.applied), f.recorded)
	}
}

// A restart resumes the recorded block: same block, same certificate, then
// broadcast, and the record is cleared.
func TestResumePendingCommitRetriesRecordedBlock(t *testing.T) {
	f := &fakeCommit{tip: 53}
	installFakeCommit(t, f)
	block := certifiedTestBlock(54, 3)
	rec, err := pendingRecordFor(block, testCert)
	if err != nil {
		t.Fatal(err)
	}
	if err := pendingCommits.Save(rec); err != nil {
		t.Fatal(err)
	}

	if err := ResumePendingCommit(context.Background(), fakeHost{}); err != nil {
		t.Fatalf("resume must commit the recorded block, got %v", err)
	}
	if len(f.applied) != 1 || f.applied[0].BlockHash != block.BlockHash || f.applied[0].Period != 3 {
		t.Fatalf("resume must apply the recorded block unchanged, got %+v", f.applied)
	}
	if f.broadcasted != 1 {
		t.Fatalf("resumed block must be broadcast once, got %d", f.broadcasted)
	}
	if got, _ := pendingCommits.Current(); got != nil {
		t.Fatalf("record must be cleared after the resumed commit, got %+v", got)
	}
}

// The block was committed but the record was not cleared before shutdown:
// resume only clears it.
func TestResumePendingCommitAlreadyCommittedOnlyClears(t *testing.T) {
	f := &fakeCommit{tip: 55, stored: storedSameApplied}
	installFakeCommit(t, f)
	rec, _ := pendingRecordFor(certifiedTestBlock(55, 0), testCert)
	_ = pendingCommits.Save(rec)

	if err := ResumePendingCommit(context.Background(), fakeHost{}); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 0 || f.broadcasted != 0 {
		t.Fatalf("an already-committed block must not be applied or broadcast again (applies=%d broadcasts=%d)", len(f.applied), f.broadcasted)
	}
	if got, _ := pendingCommits.Current(); got != nil {
		t.Fatal("record must be cleared")
	}
}

// End to end through the state machine: a certified block whose apply keeps
// failing must not advance the period (it used to: "local apply failed").
func TestBroadcastAndProcessBlockDoesNotAdvancePeriodOnApplyFailure(t *testing.T) {
	boom := errors.New("store failed")
	f := &fakeCommit{applyErrs: []error{boom, boom, boom, boom, boom}}
	installFakeCommit(t, f)
	block := certifiedTestBlock(56, 0)
	c := &Consensus{ZKBlockData: PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(block), mu: &sync.RWMutex{}}

	if err := c.BroadcastAndProcessBlock(context.Background(), testCert, true); err == nil {
		t.Fatal("a halted certified block must be reported as an error")
	}
	if got := messaging.DefaultProposerPeriods.PeriodFor(56); got != 0 {
		t.Fatalf("period advanced to %d after a failed apply of a certified block; it must stay 0", got)
	}
	if rec, _ := pendingCommits.Current(); rec == nil || rec.Height != 56 {
		t.Fatalf("pending record must hold the certified block, got %+v", rec)
	}
}

// A record left from an already-stored block (its Clear failed) must not leave
// the next certified height unprotected.
func TestCertifiedBlockReplacesStaleRecord(t *testing.T) {
	boom := errors.New("store failed")
	f := &fakeCommit{tip: 10, applyErrs: []error{boom, boom, boom, boom, boom}}
	installFakeCommit(t, f)
	stale, _ := pendingRecordFor(certifiedTestBlock(10, 0), testCert)
	if err := pendingCommits.Save(stale); err != nil {
		t.Fatal(err)
	}
	block := certifiedTestBlock(11, 0)

	if err := commitCertifiedBlock(context.Background(), block, testCert); !errors.Is(err, ErrCertifiedBlockHalted) {
		t.Fatalf("want a halt, got %v", err)
	}
	rec, _ := pendingCommits.Current()
	if rec == nil || rec.Height != 11 || rec.BlockHash != block.BlockHash.Hex() {
		t.Fatalf("height 11 must hold the pending record, got %+v", rec)
	}
	if blocked, _, _ := pendingCommits.BlocksProposal(11, 10); !blocked {
		t.Fatal("a new proposal at halted height 11 must be refused")
	}
}

// A record above the committed tip is a block that is not stored: it is kept.
func TestCertifiedBlockKeepsUncommittedRecord(t *testing.T) {
	f := &fakeCommit{tip: 9}
	installFakeCommit(t, f)
	pending, _ := pendingRecordFor(certifiedTestBlock(10, 0), testCert)
	_ = pendingCommits.Save(pending)

	_ = commitCertifiedBlock(context.Background(), certifiedTestBlock(11, 0), testCert)
	if rec, _ := pendingCommits.Current(); rec == nil || rec.Height != 10 {
		t.Fatalf("the uncommitted record for height 10 must be kept, got %+v", rec)
	}
}

// Crash window: the record is saved as soon as quorum is verified, before any
// apply attempt. A crash right after that leaves the record on disk, so new
// proposals at the height are refused and startup resumes the same block.
func TestPendingRecordSavedAtQuorumSurvivesCrashBeforeApply(t *testing.T) {
	f := &fakeCommit{tip: 59}
	installFakeCommit(t, f)
	block := certifiedTestBlock(60, 4)

	if err := recordPendingCertified(context.Background(), block, testCert); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 0 {
		t.Fatal("recording the certified block must not apply it")
	}
	if blocked, _, _ := pendingCommits.BlocksProposal(60, 59); !blocked {
		t.Fatal("a new proposal at the certified height must be refused before the commit attempt")
	}

	// Crash here; on restart the recorded block is committed unchanged.
	if err := ResumePendingCommit(context.Background(), fakeHost{}); err != nil {
		t.Fatalf("resume must commit the recorded block, got %v", err)
	}
	if len(f.applied) != 1 || f.applied[0].BlockHash != block.BlockHash || f.applied[0].Period != 4 {
		t.Fatalf("resume must apply the recorded block unchanged, got %+v", f.applied)
	}
	if rec, _ := pendingCommits.Current(); rec != nil {
		t.Fatalf("record must be cleared after the commit, got %+v", rec)
	}
}

// The normal path saves the record twice (at quorum, then in
// commitCertifiedBlock); the second save is accepted and the block commits once.
func TestCommitAfterEarlyRecordCommitsOnce(t *testing.T) {
	f := &fakeCommit{tip: 60}
	installFakeCommit(t, f)
	block := certifiedTestBlock(61, 0)
	if err := recordPendingCertified(context.Background(), block, testCert); err != nil {
		t.Fatal(err)
	}
	if err := commitCertifiedBlock(context.Background(), block, testCert); err != nil {
		t.Fatalf("commit after an early record must succeed, got %v", err)
	}
	if len(f.applied) != 1 || f.recorded != 1 {
		t.Fatalf("want one apply and one certificate record, got applies=%d recorded=%d", len(f.applied), f.recorded)
	}
	if rec, _ := pendingCommits.Current(); rec != nil {
		t.Fatalf("record must be cleared, got %+v", rec)
	}
}

// fakeHost satisfies host.Host for the resume path; broadcast is a hook, so no
// method is ever called.
type fakeHost struct{ host.Host }
