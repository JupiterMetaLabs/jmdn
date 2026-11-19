package BlockProcessing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// memAccounts is an in-memory account store behind the DB_OPs global handle.
// failRead fails reads of one address; failWrite fails writes and deletes of one.
type memAccounts struct {
	store.ThebeHandle
	mu        sync.Mutex
	accts     map[common.Address]store.Account
	readErr   error
	failRead  common.Address
	failWrite common.Address
	created   []common.Address
	deleted   []common.Address
	syncKV    map[string][]byte
	onRead    func() // called once, on the first account read
	// outboxWrites counts writes made with the outbox allowed.
	outboxWrites int
}

func (m *memAccounts) GetAccount(_ context.Context, address string) (*store.Account, error) {
	if f := m.onRead; f != nil {
		m.onRead = nil
		f()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	addr := common.HexToAddress(address)
	if m.readErr != nil || (m.failRead != common.Address{} && addr == m.failRead) {
		return nil, errors.New("connection reset by peer")
	}
	a, ok := m.accts[addr]
	if !ok {
		return nil, errors.New("sql: no rows in result set")
	}
	return &a, nil
}

func (m *memAccounts) CreateAccount(ctx context.Context, a *store.Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if thebegateway.OutboxAllowed(ctx) {
		m.outboxWrites++
	}
	if m.failWrite != (common.Address{}) && a.Address == m.failWrite {
		return errors.New("write timeout")
	}
	m.accts[a.Address] = *a
	m.created = append(m.created, a.Address)
	return nil
}

func (m *memAccounts) DeleteAccount(_ context.Context, address string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	addr := common.HexToAddress(address)
	if m.failWrite != (common.Address{}) && addr == m.failWrite {
		return errors.New("write timeout")
	}
	delete(m.accts, addr)
	m.deleted = append(m.deleted, addr)
	return nil
}

func (m *memAccounts) GetSyncKV(key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.syncKV[key], nil
}

func (m *memAccounts) PutSyncKV(key string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.syncKV == nil {
		m.syncKV = map[string][]byte{}
	}
	m.syncKV[key] = v
	return nil
}

func (m *memAccounts) writes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.created) + len(m.deleted)
}

func withMemAccounts(t *testing.T, accts map[common.Address]store.Account) *memAccounts {
	t.Helper()
	m := &memAccounts{accts: accts}
	DB_OPs.SetGlobalHandle(m)
	t.Cleanup(func() { DB_OPs.SetGlobalHandle(nil) })
	return m
}

// resetApplyHaltForTest clears the halt and keeps its record in memory for
// the test, so no store is needed.
func resetApplyHaltForTest(t *testing.T) *string {
	t.Helper()
	var recorded string
	prevRec, prevRead, prevErase := recordApplyHalt, recordedApplyHalt, eraseApplyHalt
	recordApplyHalt = func(r string) error { recorded = r; return nil }
	recordedApplyHalt = func() (string, error) { return recorded, nil }
	eraseApplyHalt = func() error { recorded = ""; return nil }
	simulateRestart()
	t.Cleanup(func() {
		recordApplyHalt, recordedApplyHalt, eraseApplyHalt = prevRec, prevRead, prevErase
		simulateRestart()
	})
	return &recorded
}

// simulateRestart drops the in-memory halt state, as a process restart does.
func simulateRestart() {
	haltState.Lock()
	haltState.reason, haltState.recorded, haltState.loaded = "", false, false
	haltState.Unlock()
}

// rollback runs a whole rollback (plan, then writes) as the apply path does
// after revoking markers.
func rollback(t *testing.T, snaps map[common.Address]AccountSnapshot) error {
	t.Helper()
	plan, err := planRollback(snaps, nil)
	if err != nil {
		return err
	}
	return restoreAfterRevoke(context.Background(), plan, nil, common.HexToHash("0xb10c"), 12)
}

func haltReason(t *testing.T) string {
	t.Helper()
	r, err := ApplyHaltReason()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var (
	acctA = common.HexToAddress("0x00000000000000000000000000000000000000a1")
	acctZ = common.HexToAddress("0x00000000000000000000000000000000000000f9")
	acctN = common.HexToAddress("0x00000000000000000000000000000000000000e7")
)

// An account the failed block created is deleted, an account it never created
// is not written at all, and an existing account is restored.
func TestRollback_DeletesCreatedAccountsRestoresExisting(t *testing.T) {
	resetApplyHaltForTest(t)
	m := withMemAccounts(t, map[common.Address]store.Account{
		acctA: {Address: acctA, Balance: "40", TxNonce: 3, TxCountSent: 3},
		acctZ: {Address: acctZ, Balance: "10"}, // created by the failed block
	})
	snaps := map[common.Address]AccountSnapshot{
		acctA: {Balance: "100", TxNonce: 2, TxCountSent: 2},
		acctZ: {Balance: "0", Absent: true},
		acctN: {Balance: "0", Absent: true}, // never created
	}
	if err := rollback(t, snaps); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.accts[acctZ]; ok {
		t.Fatal("an account the failed block created survived the rollback")
	}
	if _, ok := m.accts[acctN]; ok {
		t.Fatal("the rollback created an account that never existed")
	}
	a := m.accts[acctA]
	if a.Balance != "100" || a.TxNonce != 2 || a.TxCountSent != 2 {
		t.Fatalf("existing account not restored: %+v", a)
	}
	for _, c := range m.created {
		if c == acctZ || c == acctN {
			t.Fatalf("the rollback wrote %s, which did not exist before the block", c.Hex())
		}
	}
	if haltReason(t) != "" {
		t.Fatal("a complete rollback must not halt apply")
	}
	if m.outboxWrites != 0 {
		t.Fatalf("%d rollback writes may be retried through the outbox; none may", m.outboxWrites)
	}
}

// The rollback restores the ART identity a failed block's tx adopted, and an
// account that existed but is gone is rebuilt with its identity.
func TestRollback_RestoresIdentity(t *testing.T) {
	resetApplyHaltForTest(t)
	m := withMemAccounts(t, map[common.Address]store.Account{
		acctA: {Address: acctA, Balance: "40", Nonce: 99, DIDAddress: "did:a"}, // nonce adopted by the failed block
	})
	before := map[common.Address]AccountSnapshot{
		acctA: {Balance: "100", Nonce: 7, DIDAddress: "did:a", AccountType: "user", CreatedAt: 5},
		acctZ: {Balance: "3", Nonce: 11, DIDAddress: "did:z", AccountType: "contract", CreatedAt: 6}, // existed, now gone
	}
	if err := rollback(t, before); err != nil {
		t.Fatal(err)
	}
	if a := m.accts[acctA]; a.Nonce != 7 || a.Balance != "100" {
		t.Fatalf("existing account: nonce %d balance %s, want 7 and 100", a.Nonce, a.Balance)
	}
	z, ok := m.accts[acctZ]
	if !ok || z.Nonce != 11 || z.DIDAddress != "did:z" || z.AccountType != "contract" || z.CreatedAt != 6 || z.Balance != "3" {
		t.Fatalf("recreated account lost its identity: %+v (present=%v)", z, ok)
	}
}

// A read error stops the rollback before it writes anything, whatever the
// order of the accounts: the caller then leaves the applied+marked prefix
// untouched. Before the reads were split from the writes, most runs had
// already written or deleted other accounts when the failing read came.
func TestRollback_ReadErrorWritesNothing(t *testing.T) {
	resetApplyHaltForTest(t)
	b1 := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	b2 := common.HexToAddress("0x00000000000000000000000000000000000000b2")
	b3 := common.HexToAddress("0x00000000000000000000000000000000000000b3")
	created := common.HexToAddress("0x00000000000000000000000000000000000000c1")
	bad := common.HexToAddress("0x00000000000000000000000000000000000000dd")
	for run := 0; run < 200; run++ {
		m := withMemAccounts(t, map[common.Address]store.Account{
			b1: {Address: b1, Balance: "40"}, b2: {Address: b2, Balance: "50"}, b3: {Address: b3, Balance: "60"},
			created: {Address: created, Balance: "5"},
			bad:     {Address: bad, Balance: "70"},
		})
		m.failRead = bad
		_, err := planRollback(map[common.Address]AccountSnapshot{
			b1: {Balance: "100"}, b2: {Balance: "200"}, b3: {Balance: "300"},
			created: {Balance: "0", Absent: true},
			bad:     {Balance: "700"},
		}, nil)
		if err == nil {
			t.Fatalf("run %d: want the read error", run)
		}
		if n := m.writes(); n != 0 {
			t.Fatalf("run %d: %d accounts were written or deleted before the failing read", run, n)
		}
	}
	if haltReason(t) != "" {
		t.Fatal("a read error must not halt apply: nothing was changed")
	}
}

// A write that fails once the markers are revoked halts block apply, and the
// halt is recorded.
func TestRollback_WriteErrorAfterRevokeHalts(t *testing.T) {
	recorded := resetApplyHaltForTest(t)
	m := withMemAccounts(t, map[common.Address]store.Account{
		acctA: {Address: acctA, Balance: "40"},
		acctZ: {Address: acctZ, Balance: "10"},
	})
	m.failWrite = acctZ
	err := rollback(t, map[common.Address]AccountSnapshot{
		acctA: {Balance: "100"},
		acctZ: {Balance: "0", Absent: true},
	})
	if !errors.Is(err, ErrApplyHalted) {
		t.Fatalf("want ErrApplyHalted, got %v", err)
	}
	if haltReason(t) == "" || *recorded == "" {
		t.Fatalf("halt not in effect or not recorded: reason=%q recorded=%q", haltReason(t), *recorded)
	}
}

// A read error during rollback is returned instead of writing a blank account.
func TestRollback_ReadErrorFails(t *testing.T) {
	resetApplyHaltForTest(t)
	m := withMemAccounts(t, map[common.Address]store.Account{acctA: {Address: acctA, Balance: "1"}})
	m.readErr = errors.New("connection reset")
	if _, err := planRollback(map[common.Address]AccountSnapshot{acctA: {Balance: "5"}}, nil); err == nil || len(m.created) != 0 {
		t.Fatalf("want an error and no write; got err=%v writes=%d", err, len(m.created))
	}
}

// The snapshot records the identity fields it must restore, and a missing
// account as Absent.
func TestSnapshotAccount(t *testing.T) {
	withMemAccounts(t, map[common.Address]store.Account{
		acctA: {Address: acctA, Balance: "1", Nonce: 42, DIDAddress: "did:a", AccountType: "user", CreatedAt: 9},
	})
	s, err := snapshotAccount(nil, acctA)
	if err != nil {
		t.Fatal(err)
	}
	if s.Nonce != 42 || s.DIDAddress != "did:a" || s.AccountType != "user" || s.CreatedAt != 9 || s.Absent {
		t.Fatalf("snapshot %+v", s)
	}
	if z, err := snapshotAccount(nil, acctZ); err != nil || !z.Absent {
		t.Fatalf("a missing account must be Absent: %+v, %v", z, err)
	}
}

// Once a block committed contract state, a rollback is refused and block apply
// halts; without contract state it is not. The halt refuses every later block.
func TestRefuseIncompleteRollback_HaltsApply(t *testing.T) {
	resetApplyHaltForTest(t)
	ctx := context.Background()
	clean := common.HexToHash("0xc1")
	if err := refuseIncompleteRollback(ctx, clean, 7, errors.New("tx failed")); err != nil || haltReason(t) != "" {
		t.Fatalf("a block without contract state must roll back normally: err=%v halt=%q", err, haltReason(t))
	}

	bh := common.HexToHash("0xc2")
	markContractStateCommitted(bh)
	defer clearContractStateCommitted(bh)
	err := refuseIncompleteRollback(ctx, bh, 8, errors.New("tx failed"))
	if !errors.Is(err, ErrApplyHalted) || haltReason(t) == "" {
		t.Fatalf("want ErrApplyHalted and a halt reason, got err=%v halt=%q", err, haltReason(t))
	}

	coinbase, zkvm := acctA, acctZ
	next := &config.ZKBlock{BlockNumber: 9, BlockHash: common.HexToHash("0xc3"), CoinbaseAddr: &coinbase, ZKVMAddr: &zkvm}
	if err := ProcessBlockTransactions(ctx, next, nil); !errors.Is(err, ErrApplyHalted) {
		t.Fatalf("a halted node must refuse every later block, got %v", err)
	}
}

// A halt recorded before a restart is in effect after it, until cleared.
func TestApplyHalt_SurvivesRestartUntilCleared(t *testing.T) {
	recorded := resetApplyHaltForTest(t)
	haltApply(context.Background(), "block 8 failed")

	// Restart: the in-memory state is gone, the record is not.
	simulateRestart()
	if got := haltReason(t); got != "block 8 failed" {
		t.Fatalf("after a restart the halt reads %q, want the recorded reason", got)
	}

	if err := ClearApplyHalt(); err != nil {
		t.Fatal(err)
	}
	if haltReason(t) != "" || *recorded != "" {
		t.Fatalf("clear left halt=%q recorded=%q", haltReason(t), *recorded)
	}
	simulateRestart() // and after another restart
	if haltReason(t) != "" {
		t.Fatal("a cleared halt came back after a restart")
	}
}

// An unreadable halt record fails apply closed, and is retried on the next block.
func TestApplyHalt_UnreadableRecordFailsClosed(t *testing.T) {
	resetApplyHaltForTest(t)
	recordedApplyHalt = func() (string, error) { return "", errors.New("kv unavailable") }
	coinbase, zkvm := acctA, acctZ
	blk := &config.ZKBlock{BlockNumber: 3, BlockHash: common.HexToHash("0xd1"), CoinbaseAddr: &coinbase, ZKVMAddr: &zkvm}
	if err := ProcessBlockTransactions(context.Background(), blk, nil); err == nil || errors.Is(err, ErrApplyHalted) {
		t.Fatalf("want a fail-closed read error, got %v", err)
	}
	haltState.Lock()
	loaded := haltState.loaded
	haltState.Unlock()
	if loaded {
		t.Fatal("a failed read must be retried, not cached as 'not halted'")
	}
}

// The record lives in the node's sync KV: written, read back, and cleared.
func TestApplyHaltRecord_RoundTrip(t *testing.T) {
	withMemAccounts(t, map[common.Address]store.Account{})
	if r, err := DB_OPs.RecordedApplyHalt(); err != nil || r != "" {
		t.Fatalf("fresh store: %q, %v", r, err)
	}
	if err := DB_OPs.RecordApplyHalt("why"); err != nil {
		t.Fatal(err)
	}
	if r, err := DB_OPs.RecordedApplyHalt(); err != nil || r != "why" {
		t.Fatalf("after record: %q, %v", r, err)
	}
	if err := DB_OPs.ClearRecordedApplyHalt(); err != nil {
		t.Fatal(err)
	}
	if r, err := DB_OPs.RecordedApplyHalt(); err != nil || r != "" {
		t.Fatalf("after clear: %q, %v", r, err)
	}
}

// An abandoned rollback (reads or revocation failed) halts only when a commit
// of the block failed part-way: then accounts hold effects no marker records.
func TestRollbackAbandoned_HaltsOnlyAfterPartialCommit(t *testing.T) {
	resetApplyHaltForTest(t)
	ctx := context.Background()
	readErr := errors.New("connection reset")
	if err := rollbackAbandoned(ctx, common.HexToHash("0xe1"), 20, errors.New("stale nonce"), readErr); err != nil || haltReason(t) != "" {
		t.Fatalf("a deterministic failure leaves an applied+marked prefix: want no halt, got %v (halt %q)", err, haltReason(t))
	}
	partial := fmt.Errorf("group commit of 3 txs failed: %w", fmt.Errorf("ApplyTxAtomic x: %w: disk full", DB_OPs.ErrPartialApply))
	err := rollbackAbandoned(ctx, common.HexToHash("0xe2"), 21, partial, readErr)
	if !errors.Is(err, ErrApplyHalted) || haltReason(t) == "" {
		t.Fatalf("after a partial commit an abandoned rollback must halt, got %v (halt %q)", err, haltReason(t))
	}
}

// A halt whose record write fails is recorded by a later status check, and
// then survives a restart.
func TestApplyHalt_RecordRetriedUntilWritten(t *testing.T) {
	recorded := resetApplyHaltForTest(t)
	fail := true
	recordApplyHalt = func(r string) error {
		if fail {
			return errors.New("kv write failed")
		}
		*recorded = r
		return nil
	}
	haltApply(context.Background(), "block 8 failed")
	if r, rec, err := ApplyHaltStatus(); err != nil || r == "" || rec {
		t.Fatalf("while the write fails: reason=%q recorded=%v err=%v; want halted and not recorded", r, rec, err)
	}
	fail = false
	if r, rec, err := ApplyHaltStatus(); err != nil || r == "" || !rec || *recorded != "block 8 failed" {
		t.Fatalf("after the store recovers: reason=%q recorded=%v err=%v record=%q", r, rec, err, *recorded)
	}
	simulateRestart()
	if haltReason(t) != "block 8 failed" {
		t.Fatal("a halt recorded on retry was lifted by a restart")
	}
}

// A clear and a halt cannot interleave: whichever runs second wins in both the
// memory and the record.
func TestApplyHalt_ClearAndHaltDoNotInterleave(t *testing.T) {
	recorded := resetApplyHaltForTest(t)
	haltApply(context.Background(), "old")
	inErase, release := make(chan struct{}), make(chan struct{})
	eraseApplyHalt = func() error {
		close(inErase)
		<-release
		*recorded = ""
		return nil
	}
	cleared := make(chan error)
	go func() { cleared <- ClearApplyHalt() }()
	<-inErase
	halted := make(chan struct{})
	go func() { haltApply(context.Background(), "new"); close(halted) }()
	close(release)
	if err := <-cleared; err != nil {
		t.Fatal(err)
	}
	<-halted
	if got := haltReason(t); got != "new" || *recorded != "new" {
		t.Fatalf("memory %q and record %q disagree or lost the new halt", got, *recorded)
	}
}

// A rollback revokes the failing tx's own marker only when its commit wrote
// that marker before failing; otherwise the hash is left alone.
func TestMarkersToRevoke(t *testing.T) {
	applied := []string{"0xa", "0xb"}
	if got := markersToRevoke(applied, "0xf", errors.New("stale nonce"), true); len(got) != 2 {
		t.Fatalf("a failing tx that wrote no marker must not be revoked: %v", got)
	}
	cause := fmt.Errorf("atomic tx commit failed for 0xf: %w", fmt.Errorf("ApplyTxAtomic 0xf: %w: disk", DB_OPs.ErrApplyRedoNotCleared))
	got := markersToRevoke(applied, "0xf", cause, true)
	if len(got) != 3 || got[2] != "0xf" || len(applied) != 2 {
		t.Fatalf("want the applied txs plus the failing one, input unchanged: got %v (applied %v)", got, applied)
	}
	// The same error from flushing the previous group: the current tx never
	// ran, and the group's txs are already in applied.
	flush := fmt.Errorf("group commit of 2 txs failed: %w", fmt.Errorf("ApplyTxAtomic group: %w: disk", DB_OPs.ErrApplyRedoNotCleared))
	if got := markersToRevoke(applied, "0xf", flush, false); len(got) != 2 {
		t.Fatalf("a tx that never ran must not be revoked: %v", got)
	}
}

// A block that was applying when another block halted the node is not stored.
func TestHaltDuringApply_BlockNotStored(t *testing.T) {
	resetApplyHaltForTest(t)
	m := withMemAccounts(t, map[common.Address]store.Account{})
	m.onRead = func() { haltApply(context.Background(), "another block failed") }
	coinbase, zkvm := acctA, acctZ
	blk := &config.ZKBlock{BlockNumber: 30, BlockHash: common.HexToHash("0xf30"), CoinbaseAddr: &coinbase, ZKVMAddr: &zkvm}
	stored := false
	err := ProcessBlockTransactionsAndStore(context.Background(), blk, nil, func() error { stored = true; return nil })
	if !errors.Is(err, ErrApplyHalted) || stored {
		t.Fatalf("want ErrApplyHalted and nothing stored, got err=%v stored=%v", err, stored)
	}
}
