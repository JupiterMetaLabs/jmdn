package NodeInfo

// Tests for the payloadTypeUpdates drain path: parseUpdatesPayload validation
// and buildUpdateEntries merge semantics.
//
// These pin the two fixes for the account-corruption RCA (see RCA_account_sync.md F2):
//  1. UpdatedAt travels from the producer and is NOT re-stamped at drain time
//     (re-stamping let replayed stale entries win LWW over newer data).
//  2. Updates merge into the currently stored account instead of rebuilding a
//     defaulted object (which clobbered DIDAddress/AccountType/CreatedAt/Metadata
//     and created bogus did:<hex-address> references).

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// withAccountStub swaps the getAccountForUpdate seam for the test's duration.
func withAccountStub(t *testing.T, stub func(*config.PooledConnection, common.Address) (*DB_OPs.Account, error)) {
	t.Helper()
	orig := getAccountForUpdate
	getAccountForUpdate = stub
	t.Cleanup(func() { getAccountForUpdate = orig })
}

func decodeEntry(t *testing.T, e dbEntry) *DB_OPs.Account {
	t.Helper()
	var acc DB_OPs.Account
	if err := json.Unmarshal(e.Value, &acc); err != nil {
		t.Fatalf("unmarshal entry %s: %v", e.Key, err)
	}
	return &acc
}

const testAddr = "0x1111111111111111111111111111111111111111"

func TestParseUpdatesPayload_PreservesProducerTimestamp(t *testing.T) {
	payload := `[{"address":"` + testAddr + `","new_balance":"12345","nonce":7,"tx_nonce":8,"tx_count_sent":3,"updated_at":1700000000000000042}]`

	wires, err := parseUpdatesPayload(payload)
	if err != nil {
		t.Fatalf("parseUpdatesPayload: %v", err)
	}
	if len(wires) != 1 {
		t.Fatalf("want 1 wire, got %d", len(wires))
	}
	if wires[0].UpdatedAt != 1700000000000000042 {
		t.Fatalf("producer UpdatedAt not preserved: got %d", wires[0].UpdatedAt)
	}
}

func TestParseUpdatesPayload_InvalidBalanceIsPoison(t *testing.T) {
	payload := `[{"address":"` + testAddr + `","new_balance":"not-a-number","nonce":1}]`
	if _, err := parseUpdatesPayload(payload); err == nil {
		t.Fatal("want error for undecodable balance (poison pill), got nil")
	}
	if _, err := parseUpdatesPayload(`{broken json`); err == nil {
		t.Fatal("want error for broken JSON (poison pill), got nil")
	}
}

func TestBuildUpdateEntries_MergePreservesIdentityFields(t *testing.T) {
	addr := common.HexToAddress(testAddr)
	stored := &DB_OPs.Account{
		DIDAddress:  "did:jmdn:realdid",
		Address:     addr,
		Balance:     "999",
		Nonce:       1,
		TxNonce:     2,
		TxCountSent: 1,
		AccountType: "validator",
		CreatedAt:   111,
		UpdatedAt:   222,
		Metadata:    map[string]interface{}{"tag": "keep-me"},
	}
	withAccountStub(t, func(_ *config.PooledConnection, a common.Address) (*DB_OPs.Account, error) {
		if a != addr {
			return nil, DB_OPs.ErrNotFound
		}
		cp := *stored
		return &cp, nil
	})

	producerTS := int64(1700000000000000042)
	entries, err := buildUpdateEntries(context.Background(), nil, []accountUpdateWire{{
		Address: testAddr, NewBalance: "5000", Nonce: 9, TxNonce: 10, TxCountSent: 4, UpdatedAt: producerTS,
	}})
	if err != nil {
		t.Fatalf("buildUpdateEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries (address: + did:), got %d", len(entries))
	}
	if entries[0].Key != DB_OPs.Prefix+addr.Hex() {
		t.Errorf("address key: got %s", entries[0].Key)
	}
	if entries[1].Key != DB_OPs.DIDPrefix+"did:jmdn:realdid" {
		t.Errorf("did key: got %s", entries[1].Key)
	}

	acc := decodeEntry(t, entries[0])
	// Updated by the wire:
	if acc.Balance != "5000" || acc.Nonce != 9 || acc.TxNonce != 10 || acc.TxCountSent != 4 {
		t.Errorf("wire fields not applied: %+v", acc)
	}
	// LWW key must be the PRODUCER timestamp, never drain-time now():
	if acc.UpdatedAt != producerTS {
		t.Errorf("UpdatedAt: want producer %d, got %d (drain-time re-stamp bug)", producerTS, acc.UpdatedAt)
	}
	// Identity fields preserved (the pre-fix code clobbered all of these):
	if acc.DIDAddress != "did:jmdn:realdid" {
		t.Errorf("DIDAddress clobbered: %q", acc.DIDAddress)
	}
	if acc.AccountType != "validator" {
		t.Errorf("AccountType clobbered: %q", acc.AccountType)
	}
	if acc.CreatedAt != 111 {
		t.Errorf("CreatedAt clobbered: %d", acc.CreatedAt)
	}
	if v, ok := acc.Metadata["tag"]; !ok || v != "keep-me" {
		t.Errorf("Metadata clobbered: %+v", acc.Metadata)
	}
}

func TestBuildUpdateEntries_NewAccountHasNoBogusDID(t *testing.T) {
	withAccountStub(t, func(_ *config.PooledConnection, _ common.Address) (*DB_OPs.Account, error) {
		return nil, DB_OPs.ErrNotFound
	})

	producerTS := int64(1700000000000000042)
	entries, err := buildUpdateEntries(context.Background(), nil, []accountUpdateWire{{
		Address: testAddr, NewBalance: "42", Nonce: 1, UpdatedAt: producerTS,
	}})
	if err != nil {
		t.Fatalf("buildUpdateEntries: %v", err)
	}
	// Exactly one entry: the pre-fix code set DIDAddress = hex address, creating
	// a bogus did:0x... reference. New accounts must emit the address key only.
	if len(entries) != 1 {
		t.Fatalf("want 1 entry (no did: ref for new account), got %d", len(entries))
	}
	acc := decodeEntry(t, entries[0])
	if acc.DIDAddress != "" {
		t.Errorf("new account must have empty DIDAddress, got %q", acc.DIDAddress)
	}
	if acc.Balance != "42" || acc.UpdatedAt != producerTS || acc.CreatedAt != producerTS {
		t.Errorf("new account fields wrong: %+v", acc)
	}
}

func TestBuildUpdateEntries_ZeroTimestampFallsBackToNow(t *testing.T) {
	// In-flight entries enqueued by a pre-upgrade producer carry updated_at=0;
	// the drain must still produce a usable LWW timestamp.
	withAccountStub(t, func(_ *config.PooledConnection, _ common.Address) (*DB_OPs.Account, error) {
		return nil, DB_OPs.ErrNotFound
	})

	before := time.Now().UTC().UnixNano()
	entries, err := buildUpdateEntries(context.Background(), nil, []accountUpdateWire{{
		Address: testAddr, NewBalance: "1", UpdatedAt: 0,
	}})
	after := time.Now().UTC().UnixNano()
	if err != nil {
		t.Fatalf("buildUpdateEntries: %v", err)
	}
	acc := decodeEntry(t, entries[0])
	if acc.UpdatedAt < before || acc.UpdatedAt > after {
		t.Errorf("zero-timestamp fallback: UpdatedAt %d not in [%d, %d]", acc.UpdatedAt, before, after)
	}
}

func TestBuildUpdateEntries_DBErrorRetriesWholeBatch(t *testing.T) {
	// Any non-not-found DB error must fail the call — entries stay unACKed in
	// the PEL and replay. Applying a partial batch would drop updates silently.
	withAccountStub(t, func(_ *config.PooledConnection, _ common.Address) (*DB_OPs.Account, error) {
		return nil, fmt.Errorf("immudb: connection reset")
	})

	if _, err := buildUpdateEntries(context.Background(), nil, []accountUpdateWire{{
		Address: testAddr, NewBalance: "1", UpdatedAt: 1,
	}}); err == nil {
		t.Fatal("want error on DB read failure (batch must retry), got nil")
	}
}
