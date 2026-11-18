package BlockProcessing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/store"
	"gossipnode/config"
)

// memAccounts is an in-memory account store behind the DB_OPs global handle.
type memAccounts struct {
	store.ThebeHandle
	mu      sync.Mutex
	accts   map[common.Address]store.Account
	readErr error
	created []common.Address
	deleted []common.Address
}

func (m *memAccounts) GetAccount(_ context.Context, address string) (*store.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readErr != nil {
		return nil, m.readErr
	}
	a, ok := m.accts[common.HexToAddress(address)]
	if !ok {
		return nil, errors.New("sql: no rows in result set")
	}
	return &a, nil
}

func (m *memAccounts) CreateAccount(_ context.Context, a *store.Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accts[a.Address] = *a
	m.created = append(m.created, a.Address)
	return nil
}

func (m *memAccounts) DeleteAccount(_ context.Context, address string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	addr := common.HexToAddress(address)
	delete(m.accts, addr)
	m.deleted = append(m.deleted, addr)
	return nil
}

func (m *memAccounts) GetSyncKV(string) ([]byte, error) { return nil, nil }

func withMemAccounts(t *testing.T, accts map[common.Address]store.Account) *memAccounts {
	t.Helper()
	m := &memAccounts{accts: accts}
	DB_OPs.SetGlobalHandle(m)
	t.Cleanup(func() { DB_OPs.SetGlobalHandle(nil) })
	return m
}

func resetApplyHaltForTest(t *testing.T) {
	t.Helper()
	applyHaltReason.Store(nil)
	t.Cleanup(func() { applyHaltReason.Store(nil) })
}

var (
	acctA = common.HexToAddress("0x00000000000000000000000000000000000000a1")
	acctZ = common.HexToAddress("0x00000000000000000000000000000000000000f9")
	acctN = common.HexToAddress("0x00000000000000000000000000000000000000e7")
)

// An account the failed block created is deleted, an account it never created
// is not written at all, and an existing account is restored.
func TestRollbackState_DeletesCreatedAccountsRestoresExisting(t *testing.T) {
	m := withMemAccounts(t, map[common.Address]store.Account{
		acctA: {Address: acctA, Balance: "40", TxNonce: 3, TxCountSent: 3},
		acctZ: {Address: acctZ, Balance: "10"}, // created by the failed block
	})
	snaps := map[common.Address]AccountSnapshot{
		acctA: {Balance: "100", TxNonce: 2, TxCountSent: 2},
		acctZ: {Balance: "0", Absent: true},
		acctN: {Balance: "0", Absent: true}, // never created
	}
	if err := rollbackState(context.Background(), snaps, nil); err != nil {
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
}

// A read error during rollback fails it instead of writing a blank account.
func TestRollbackState_ReadErrorFails(t *testing.T) {
	m := withMemAccounts(t, map[common.Address]store.Account{acctA: {Address: acctA, Balance: "1"}})
	m.readErr = errors.New("connection reset")
	err := rollbackState(context.Background(), map[common.Address]AccountSnapshot{acctA: {Balance: "5"}}, nil)
	if err == nil || len(m.created) != 0 {
		t.Fatalf("want an error and no write; got err=%v writes=%d", err, len(m.created))
	}
}

// A contract tx's touched accounts join the snapshot before it commits: the
// first capture wins, a missing account is Absent, and a read error fails.
func TestRecordRollbackTargets(t *testing.T) {
	m := withMemAccounts(t, map[common.Address]store.Account{
		acctA: {Address: acctA, Balance: "100", TxNonce: 1},
	})
	snaps := map[common.Address]AccountSnapshot{acctA: {Balance: "999"}} // already captured at block start
	ctx := withRollbackSet(context.Background(), &rollbackSet{snaps: snaps})
	if err := recordRollbackTargets(ctx, nil, []common.Address{acctA, acctZ}); err != nil {
		t.Fatal(err)
	}
	if snaps[acctA].Balance != "999" {
		t.Fatalf("a later capture replaced the block-start snapshot: %+v", snaps[acctA])
	}
	if z := snaps[acctZ]; !z.Absent {
		t.Fatalf("a missing account must be Absent: %+v", z)
	}

	m.readErr = errors.New("timeout")
	if err := recordRollbackTargets(ctx, nil, []common.Address{acctN}); err == nil {
		t.Fatal("a read error must fail the capture, not record the account as absent")
	}
	if err := recordRollbackTargets(context.Background(), nil, []common.Address{acctN}); err != nil {
		t.Fatalf("outside a block apply it must do nothing, got %v", err)
	}
}

// Once a block committed contract state, a rollback is refused and block apply
// is halted for the process; without contract state it is not.
func TestRefuseIncompleteRollback_HaltsApply(t *testing.T) {
	resetApplyHaltForTest(t)
	clean := common.HexToHash("0xc1")
	if err := refuseIncompleteRollback(clean, 7, errors.New("tx failed")); err != nil || ApplyHaltReason() != "" {
		t.Fatalf("a block without contract state must roll back normally: err=%v halt=%q", err, ApplyHaltReason())
	}

	bh := common.HexToHash("0xc2")
	markContractStateCommitted(bh)
	defer clearContractStateCommitted(bh)
	err := refuseIncompleteRollback(bh, 8, errors.New("tx failed"))
	if !errors.Is(err, ErrApplyHalted) || ApplyHaltReason() == "" {
		t.Fatalf("want ErrApplyHalted and a halt reason, got err=%v halt=%q", err, ApplyHaltReason())
	}

	coinbase, zkvm := acctA, acctZ
	next := &config.ZKBlock{BlockNumber: 9, BlockHash: common.HexToHash("0xc3"), CoinbaseAddr: &coinbase, ZKVMAddr: &zkvm}
	if err := ProcessBlockTransactions(context.Background(), next, nil); !errors.Is(err, ErrApplyHalted) {
		t.Fatalf("a halted node must refuse every later block, got %v", err)
	}
}

// The rollback restores the ART identity a failed block's tx adopted, and an
// account that existed but is gone is rebuilt with its identity.
func TestRollbackState_RestoresIdentity(t *testing.T) {
	m := withMemAccounts(t, map[common.Address]store.Account{
		acctA: {Address: acctA, Balance: "40", Nonce: 99, DIDAddress: "did:a"}, // nonce adopted by the failed block
	})
	before := map[common.Address]AccountSnapshot{
		acctA: {Balance: "100", Nonce: 7, DIDAddress: "did:a", AccountType: "user", CreatedAt: 5},
		acctZ: {Balance: "3", Nonce: 11, DIDAddress: "did:z", AccountType: "contract", CreatedAt: 6}, // existed, now gone
	}
	if err := rollbackState(context.Background(), before, nil); err != nil {
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

// The snapshot records the identity fields it must restore.
func TestSnapshotAccount_CapturesIdentity(t *testing.T) {
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
}
