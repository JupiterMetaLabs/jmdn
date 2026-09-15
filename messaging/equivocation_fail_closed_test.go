package messaging

// Both equivocation store paths must FAIL CLOSED (audit CON-08 read, CON-21
// write).
//
// # Why this file exists, and why it asserts on strings
//
// These guards were fixed once and silently un-fixed once. 0167cd21 (read) and
// 1cfcc76d (write) landed; merge 524fe714 then resolved this hunk to the
// pre-fix side, restoring log-and-continue on both paths. Both fix commits
// stayed ancestors of the branch, so `git merge-base --is-ancestor` — and every
// human "was that merged?" check — answered YES for five weeks while the
// shipped binary had no equivocation defence when the store was unhealthy.
//
// The lesson is that a merge graph cannot detect that class of revert. Only
// behaviour can. So these tests assert the REJECTION REASON STRINGS that only
// the fail-closed branches can produce: delete the guards and these fail,
// whatever the history says.
//
// equivocation_persist_test.go covers the healthy-store paths; its fake store
// never returns an error, so it never reaches the branches tested here.

import (
	"context"
	"errors"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/crypto"
)

// errEquivStore is an EquivocationStore whose read and write paths fail on
// demand, so the unhealthy-store branches are reachable from a test.
type errEquivStore struct {
	m        map[uint64]string
	readErr  error
	writeErr error
}

func newErrEquivStore() *errEquivStore { return &errEquivStore{m: map[uint64]string{}} }

func (f *errEquivStore) FirstSeenHash(height uint64) (string, bool, error) {
	if f.readErr != nil {
		return "", false, f.readErr
	}
	v, ok := f.m[height]
	return v, ok, nil
}

func (f *errEquivStore) RecordFirstSeen(height uint64, hashHex string) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	if _, ok := f.m[height]; !ok {
		f.m[height] = hashHex
	}
	return nil
}

// failClosedBlock mirrors p6Block in equivocation_persist_test.go: a block whose
// BlockHash is the canonical hash of its txs, so body binding passes and
// validation reaches checkEquivocation (which runs last).
func failClosedBlock(num uint64, txs ...config.Transaction) *config.ZKBlock {
	return &config.ZKBlock{
		BlockHash:    RecomputeBlockHashFromTxs(txs),
		TxnsRoot:     RecomputeTxnsRoot(txs),
		BlockNumber:  num,
		Transactions: txs,
	}
}

// TestEquivocationReadErrorFailsClosed — CON-08.
//
// A durable READ error must reject. It must NOT fall through to the
// "first sighting" branch: after a restart seenHeights is empty, so the durable
// read is the only equivocation defence there is. Degrading to in-memory at
// exactly the moment the store is unhealthy does not weaken detection, it
// removes it, and silently.
func TestEquivocationReadErrorFailsClosed(t *testing.T) {
	ctx := context.Background()

	store := newErrEquivStore()
	store.readErr = errors.New("simulated durable read failure")
	SetEquivocationStore(store)
	t.Cleanup(func() { SetEquivocationStore(nil) })
	resetEquivocation()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}

	b := failClosedBlock(70, signedTx(t, key, 0))
	m := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}

	rej := validateRemoteBlock(ctx, m)
	if rej == nil {
		t.Fatal("a block was ACCEPTED while the durable equivocation store was unreadable — " +
			"after a restart that store is the only equivocation defence, so this accepts " +
			"both halves of a fork (audit CON-08, reverted once by merge 524fe714)")
	}
	if rej.reason != "equivocation_unreadable" {
		t.Fatalf("want rejection reason %q, got %q (err: %v)",
			"equivocation_unreadable", rej.reason, rej.err)
	}
}

// TestEquivocationWriteErrorFailsClosed — CON-21.
//
// A durable WRITE error must reject too. A failed write leaves a hole the
// fail-closed read cannot detect: the later read succeeds and returns
// not-found, so a genuinely conflicting block is treated as a first sighting
// and no error is ever raised. Fixing the read alone leaves the store quietly
// developing gaps.
func TestEquivocationWriteErrorFailsClosed(t *testing.T) {
	ctx := context.Background()

	store := newErrEquivStore()
	store.writeErr = errors.New("simulated durable write failure")
	SetEquivocationStore(store)
	t.Cleanup(func() { SetEquivocationStore(nil) })
	resetEquivocation()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}

	b := failClosedBlock(71, signedTx(t, key, 0))
	m := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}

	rej := validateRemoteBlock(ctx, m)
	if rej == nil {
		t.Fatal("a block was ACCEPTED after its durable equivocation record failed to write — " +
			"that height now has no durable marker, so a conflicting block at the same " +
			"height reads as a first sighting after a restart (audit CON-21)")
	}
	if rej.reason != "equivocation_write_failed" {
		t.Fatalf("want rejection reason %q, got %q (err: %v)",
			"equivocation_write_failed", rej.reason, rej.err)
	}
}

// TestEquivocationWriteErrorLeavesNoInMemoryRecord pins the ORDERING half of
// the CON-21 fix, which the reason-string assertions above cannot see.
//
// seenHeights must be written only AFTER the durable write succeeds. If the
// in-memory cache is populated first (as the reverted code did), a failed write
// leaves the two stores disagreeing: this process believes height N is recorded
// while nothing is durable. The node then accepts the conflicting block at
// height N after the next restart AND, before that restart, reports the first
// hash as authoritative from a cache no durable record backs.
func TestEquivocationWriteErrorLeavesNoInMemoryRecord(t *testing.T) {
	ctx := context.Background()

	store := newErrEquivStore()
	store.writeErr = errors.New("simulated durable write failure")
	SetEquivocationStore(store)
	t.Cleanup(func() { SetEquivocationStore(nil) })
	resetEquivocation()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}

	b := failClosedBlock(72, signedTx(t, key, 0))
	m := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}

	if rej := validateRemoteBlock(ctx, m); rej == nil {
		t.Fatal("expected a rejection when the durable write failed")
	}

	seenHeightsMu.Lock()
	cached, present := seenHeights[72]
	seenHeightsMu.Unlock()

	if present {
		t.Fatalf("height 72 was cached in-memory as %q even though its durable write FAILED — "+
			"the in-memory map and the durable store now disagree, which is the split "+
			"CON-21's ordering fix exists to prevent", cached)
	}
}

// TestEquivocationHealthyStoreStillPasses is the control.
//
// Without it the three tests above would pass just as happily against a
// checkEquivocation that rejected unconditionally. This proves the guards fire
// on store errors specifically, not on everything.
func TestEquivocationHealthyStoreStillPasses(t *testing.T) {
	ctx := context.Background()

	store := newErrEquivStore() // no readErr, no writeErr: healthy
	SetEquivocationStore(store)
	t.Cleanup(func() { SetEquivocationStore(nil) })
	resetEquivocation()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}

	b := failClosedBlock(73, signedTx(t, key, 0))
	m := config.BlockMessage{Block: b, Data: blockBoundCert(t, b, "peerA", "peerB", "peerC")}

	if rej := validateRemoteBlock(ctx, m); rej != nil {
		t.Fatalf("a valid block with a HEALTHY store must pass, got %s: %v", rej.reason, rej.err)
	}

	// And the happy path must record both, in the documented order.
	if _, ok := store.m[73]; !ok {
		t.Fatal("height 73 should have been recorded durably")
	}
	seenHeightsMu.Lock()
	_, present := seenHeights[73]
	seenHeightsMu.Unlock()
	if !present {
		t.Fatal("height 73 should be cached in-memory after a successful durable write")
	}
}
