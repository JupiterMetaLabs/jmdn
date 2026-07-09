package DB_OPs

// Unit tests for mergeAccountForWrite — the single pure decision point for all
// account writes through BatchRestoreAccounts. Ports the merge-semantics tests
// that previously lived against the worker's buildUpdateEntries (dropped when
// the merge moved into this package), and pins the LWW/monotonic/new-default
// behaviour from the account-corruption RCA (F2).

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

var mergeTestAddr = common.HexToAddress("0x1111111111111111111111111111111111111111")

func storedAccount() *Account {
	return &Account{
		DIDAddress:  "did:jmdn:realdid",
		Address:     mergeTestAddr,
		Balance:     "999",
		Nonce:       5,
		TxNonce:     12,
		TxCountSent: 9,
		AccountType: "validator",
		CreatedAt:   111,
		UpdatedAt:   222, // legacy small value — treated as seconds by normalization
		Metadata:    map[string]interface{}{"tag": "keep-me"},
	}
}

// sparseUpdate mimics what updateWiresToEntries produces: balance/nonce fields
// only, identity fields zero-valued, producer timestamp in nanos.
func sparseUpdate(ts int64) Account {
	return Account{
		Address:     mergeTestAddr,
		Balance:     "5000",
		Nonce:       0, // receiver-only recon delta carries no ART nonce
		TxNonce:     10,
		TxCountSent: 4,
		UpdatedAt:   ts,
	}
}

func TestMergeAccountForWrite_PreservesIdentityFields(t *testing.T) {
	existing := storedAccount()
	producerTS := time.Now().UTC().UnixNano()

	merged, write := mergeAccountForWrite(existing, sparseUpdate(producerTS))
	if !write {
		t.Fatal("newer incoming must be written")
	}
	// Wire fields applied:
	if merged.Balance != "5000" {
		t.Errorf("balance not applied: %q", merged.Balance)
	}
	// LWW key must be the producer timestamp:
	if merged.UpdatedAt != producerTS {
		t.Errorf("UpdatedAt: want %d, got %d", producerTS, merged.UpdatedAt)
	}
	// Identity fields preserved (pre-fix code clobbered all of these):
	if merged.DIDAddress != "did:jmdn:realdid" {
		t.Errorf("DIDAddress clobbered: %q", merged.DIDAddress)
	}
	if merged.AccountType != "validator" {
		t.Errorf("AccountType clobbered: %q", merged.AccountType)
	}
	if merged.CreatedAt != 111 {
		t.Errorf("CreatedAt clobbered: %d", merged.CreatedAt)
	}
	if v, ok := merged.Metadata["tag"]; !ok || v != "keep-me" {
		t.Errorf("Metadata clobbered: %+v", merged.Metadata)
	}
}

func TestMergeAccountForWrite_MonotonicCounterGuards(t *testing.T) {
	existing := storedAccount()
	merged, write := mergeAccountForWrite(existing, sparseUpdate(time.Now().UTC().UnixNano()))
	if !write {
		t.Fatal("newer incoming must be written")
	}
	// ART nonce 0 = producer had no value — never zero it:
	if merged.Nonce != 5 {
		t.Errorf("ART nonce zeroed: got %d, want 5", merged.Nonce)
	}
	// Tx counters never decrease (incoming 10/4 < existing 12/9):
	if merged.TxNonce != 12 {
		t.Errorf("TxNonce regressed: got %d, want 12", merged.TxNonce)
	}
	if merged.TxCountSent != 9 {
		t.Errorf("TxCountSent regressed: got %d, want 9", merged.TxCountSent)
	}

	// Higher incoming counters pass through:
	upd := sparseUpdate(time.Now().UTC().UnixNano())
	upd.Nonce = 7
	upd.TxNonce = 13
	upd.TxCountSent = 10
	merged, _ = mergeAccountForWrite(existing, upd)
	if merged.Nonce != 7 || merged.TxNonce != 13 || merged.TxCountSent != 10 {
		t.Errorf("higher incoming counters must apply: %+v", merged)
	}
}

func TestMergeAccountForWrite_ForgedDIDStripped(t *testing.T) {
	existing := storedAccount()
	upd := sparseUpdate(time.Now().UTC().UnixNano())
	// Legacy forged DID: lowercase hex address (Address.Hex() is checksummed,
	// so a case-sensitive compare misses it — the original mitigation bug).
	upd.DIDAddress = "0x1111111111111111111111111111111111111111"
	upd.AccountType = "user" // legacy hardcoded placeholder

	merged, write := mergeAccountForWrite(existing, upd)
	if !write {
		t.Fatal("newer incoming must be written")
	}
	if merged.DIDAddress != "did:jmdn:realdid" {
		t.Errorf("forged hex DID not stripped: %q", merged.DIDAddress)
	}
	if merged.AccountType != "validator" {
		t.Errorf("legacy 'user' placeholder must not clobber real type: %q", merged.AccountType)
	}
}

func TestMergeAccountForWrite_NewAccountDefaults(t *testing.T) {
	producerTS := time.Now().UTC().UnixNano()
	merged, write := mergeAccountForWrite(nil, sparseUpdate(producerTS))
	if !write {
		t.Fatal("new account must be written")
	}
	if merged.DIDAddress != "" {
		t.Errorf("new account must have empty DIDAddress (no bogus did:0x refs), got %q", merged.DIDAddress)
	}
	if merged.AccountType != "user" {
		t.Errorf("new account default AccountType: got %q, want user", merged.AccountType)
	}
	if merged.CreatedAt != producerTS {
		t.Errorf("new account CreatedAt: got %d, want %d", merged.CreatedAt, producerTS)
	}
	// Full account objects (accounts payload) keep their own values:
	full := *storedAccount()
	merged, _ = mergeAccountForWrite(nil, full)
	if merged.AccountType != "validator" || merged.CreatedAt != 111 {
		t.Errorf("full object defaults must not be overridden: %+v", merged)
	}
}

func TestMergeAccountForWrite_LWWUnitSafe(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	// Live executor write (block timestamp, SECONDS) one minute AFTER a sync
	// write (nanos): the stored live value must win — raw comparison would let
	// the nano-stamped incoming beat it by 9 orders of magnitude.
	existing := storedAccount()
	existing.UpdatedAt = base.Add(time.Minute).Unix() // seconds, later
	_, write := mergeAccountForWrite(existing, sparseUpdate(base.UnixNano()))
	if write {
		t.Error("older sync write (nanos) must lose LWW to newer live write (seconds)")
	}

	// Reverse: sync write after the live write must win.
	existing.UpdatedAt = base.Unix()
	_, write = mergeAccountForWrite(existing, sparseUpdate(base.Add(time.Minute).UnixNano()))
	if !write {
		t.Error("newer sync write must beat older live write after normalization")
	}
}

func TestMergeAccountForWrite_EqualTimestampSameBalanceSkips(t *testing.T) {
	existing := storedAccount()
	ts := time.Now().UTC().UnixNano()
	existing.UpdatedAt = ts

	upd := sparseUpdate(ts)
	upd.Balance = existing.Balance
	if _, write := mergeAccountForWrite(existing, upd); write {
		t.Error("identical timestamp + balance must skip the write (idempotent replay)")
	}

	upd.Balance = "different"
	if _, write := mergeAccountForWrite(existing, upd); !write {
		t.Error("identical timestamp but different balance must write")
	}
}
