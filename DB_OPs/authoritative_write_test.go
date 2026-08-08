package DB_OPs

// Regression tests for KB-review findings 1-2 (2026-08-06): the two
// authoritative balance writers must BYPASS the LWW merge gate, while
// uncoordinated sync writers must stay behind it. Without the bypass, a
// stored account carrying a newer wall-clock UpdatedAt silently swallowed
// reconciliation deltas for older blocks while their tx_processed markers
// were still written — permanently lost credits.

import (
	"context"
	"encoding/json"
	"testing"

	"gossipnode/DB_OPs/store"

	"github.com/ethereum/go-ethereum/common"
)

// memAccountsHandle is a minimal ThebeHandle double: accounts in a map,
// everything else falls through to the nil embedded interface (panics if
// unexpectedly touched — same pattern as the WAL writeback test double).
type memAccountsHandle struct {
	store.ThebeHandle
	accounts map[string]*store.Account
}

func (m *memAccountsHandle) GetAccount(_ context.Context, address string) (*store.Account, error) {
	return m.accounts[address], nil
}

func (m *memAccountsHandle) CreateAccount(_ context.Context, a *store.Account) error {
	cp := *a
	m.accounts[a.Address.Hex()] = &cp
	return nil
}

func (m *memAccountsHandle) Close() error { return nil }

func seedHandle(t *testing.T, addr common.Address, balance string, updatedAt int64) *memAccountsHandle {
	t.Helper()
	h := &memAccountsHandle{accounts: map[string]*store.Account{
		addr.Hex(): {Address: addr, Balance: balance, UpdatedAt: updatedAt, AccountType: "user"},
	}}
	SetGlobalHandle(h)
	t.Cleanup(func() { SetGlobalHandle(nil) })
	return h
}

func encodeDoc(t *testing.T, addr common.Address, balance string, updatedAt int64) struct {
	Key   string
	Value []byte
} {
	t.Helper()
	doc := &Account{Address: addr, Balance: balance, UpdatedAt: updatedAt, AccountType: "user"}
	val, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return struct {
		Key   string
		Value []byte
	}{Key: Prefix + addr.Hex(), Value: val}
}

func TestAuthoritativeWrite_BypassesLWW_OlderBlockStillApplies(t *testing.T) {
	addr := common.HexToAddress("0x5555555555555555555555555555555555555555")
	// Stored doc stamped FAR in the future of the incoming block timestamp —
	// the exact shape that made recon deltas vanish (wall-clock vs block-ts).
	h := seedHandle(t, addr, "1000", 9_000_000_000_000_000_000)

	entry := encodeDoc(t, addr, "1500", 1_700_000_000) // old block ts (seconds)
	if err := BatchPutAccountsAuthoritative([]struct {
		Key   string
		Value []byte
	}{entry}); err != nil {
		t.Fatalf("authoritative write failed: %v", err)
	}
	got := h.accounts[addr.Hex()]
	if got == nil || got.Balance != "1500" {
		t.Fatalf("authoritative write did not win: got %+v, want balance 1500", got)
	}
}

func TestSyncRestore_StaysBehindLWW_OlderWriteDropped(t *testing.T) {
	addr := common.HexToAddress("0x6666666666666666666666666666666666666666")
	h := seedHandle(t, addr, "1000", 9_000_000_000_000_000_000)

	entry := encodeDoc(t, addr, "0", 1_700_000_000) // stale sync placeholder
	if err := BatchRestoreAccounts(context.Background(), nil, []struct {
		Key   string
		Value []byte
	}{entry}); err != nil {
		t.Fatalf("BatchRestoreAccounts errored: %v", err)
	}
	got := h.accounts[addr.Hex()]
	if got == nil || got.Balance != "1000" {
		t.Fatalf("LWW gate failed to protect stored balance: got %+v, want 1000", got)
	}
}
