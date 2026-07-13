package NodeInfo

// Tests for the payloadTypeUpdates drain path: parseUpdatesPayload validation
// and updateWiresToEntries conversion.
//
// These pin two fixes for a historical account-corruption bug:
//  1. UpdatedAt travels from the producer and is NOT re-stamped at drain time
//     (re-stamping let replayed stale entries win LWW over newer data).
//  2. Updates are written as SPARSE objects — identity fields (DIDAddress,
//     AccountType, CreatedAt, Metadata) stay zero-valued and are merged from the
//     stored account inside BatchRestoreAccounts. The pre-fix code rebuilt a
//     defaulted object (DIDAddress = hex address, AccountType = "user"), which
//     clobbered real account objects and created bogus did:<hex-address> refs.

import (
	"encoding/json"
	"testing"
	"time"

	"gossipnode/DB_OPs"
)

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

func TestUpdateWiresToEntries_SparseObjectCarriesProducerTimestamp(t *testing.T) {
	producerTS := int64(1700000000000000042)
	entries, err := updateWiresToEntries([]accountUpdateWire{{
		Address: testAddr, NewBalance: "5000", Nonce: 9, TxNonce: 10, TxCountSent: 4, UpdatedAt: producerTS,
	}})
	if err != nil {
		t.Fatalf("updateWiresToEntries: %v", err)
	}
	// Exactly one entry: updates emit the address key only. The pre-fix code set
	// DIDAddress = hex address, creating a bogus did:0x... reference.
	if len(entries) != 1 {
		t.Fatalf("want 1 entry (address key only), got %d", len(entries))
	}

	acc := decodeEntry(t, entries[0])
	// Wire fields applied:
	if acc.Balance != "5000" || acc.Nonce != 9 || acc.TxNonce != 10 || acc.TxCountSent != 4 {
		t.Errorf("wire fields not applied: %+v", acc)
	}
	// LWW key must be the PRODUCER timestamp, never drain-time now():
	if acc.UpdatedAt != producerTS {
		t.Errorf("UpdatedAt: want producer %d, got %d (drain-time re-stamp bug)", producerTS, acc.UpdatedAt)
	}
	// Identity fields must be zero-valued — BatchRestoreAccounts merges them from
	// the stored account. Non-zero values here would clobber real data.
	if acc.DIDAddress != "" {
		t.Errorf("DIDAddress must be empty (merge owns identity), got %q", acc.DIDAddress)
	}
	if acc.AccountType != "" {
		t.Errorf("AccountType must be empty (merge owns identity), got %q", acc.AccountType)
	}
	if acc.CreatedAt != 0 {
		t.Errorf("CreatedAt must be zero (merge owns identity), got %d", acc.CreatedAt)
	}
	if acc.Metadata != nil {
		t.Errorf("Metadata must be nil (merge owns identity), got %+v", acc.Metadata)
	}
}

func TestUpdateWiresToEntries_ZeroTimestampFallsBackToNow(t *testing.T) {
	// In-flight entries enqueued by a pre-upgrade producer carry updated_at=0;
	// the drain must still produce a usable LWW timestamp.
	before := time.Now().UTC().UnixNano()
	entries, err := updateWiresToEntries([]accountUpdateWire{{
		Address: testAddr, NewBalance: "1", UpdatedAt: 0,
	}})
	after := time.Now().UTC().UnixNano()
	if err != nil {
		t.Fatalf("updateWiresToEntries: %v", err)
	}
	acc := decodeEntry(t, entries[0])
	if acc.UpdatedAt < before || acc.UpdatedAt > after {
		t.Errorf("zero-timestamp fallback: UpdatedAt %d not in [%d, %d]", acc.UpdatedAt, before, after)
	}
}
