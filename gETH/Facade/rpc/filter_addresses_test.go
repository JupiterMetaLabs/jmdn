package rpc

// eth_getLogs took an unbounded, undeduplicated address list. Each address is a
// separate index scan in logstore.GetLogs, and the `seen` map there
// deduplicates RESULTS, not SCANS — so listing one address N times cost N full
// scans of the same contract, from a single unauthenticated request, and the
// batch endpoint multiplied that again.

import (
	"fmt"
	"testing"
)

func addrList(n int, same bool) []any {
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		if same {
			out = append(out, "0x00000000000000000000000000000000000000a1")
			continue
		}
		out = append(out, fmt.Sprintf("0x%040x", i+1))
	}
	return out
}

// Repeating one address must cost one scan, not N.
func TestToFilterQuery_DeduplicatesAddresses(t *testing.T) {
	q, err := toFilterQuery(map[string]any{"address": addrList(50, true)})
	if err != nil {
		t.Fatalf("a repeated address is a valid filter, got %v", err)
	}
	if len(q.Addresses) != 1 {
		t.Fatalf("50 copies of one address must collapse to 1, got %d", len(q.Addresses))
	}
}

// Deduplication is on the normalised form, because that is what the scan key
// uses (logstore lower-cases the address to build the prefix).
func TestToFilterQuery_DeduplicatesCaseInsensitively(t *testing.T) {
	q, err := toFilterQuery(map[string]any{"address": []any{
		"0x00000000000000000000000000000000000000AB",
		"0x00000000000000000000000000000000000000ab",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(q.Addresses) != 1 {
		t.Fatalf("same address in two cases must collapse to 1, got %d", len(q.Addresses))
	}
}

// Above the cap the query is refused rather than silently truncated: a client
// asking for more than the node will serve should be told.
func TestToFilterQuery_CapsAddressList(t *testing.T) {
	limit := maxFilterAddresses()

	if _, err := toFilterQuery(map[string]any{"address": addrList(limit, false)}); err != nil {
		t.Fatalf("exactly the cap (%d) must be accepted, got %v", limit, err)
	}
	if _, err := toFilterQuery(map[string]any{"address": addrList(limit+1, false)}); err == nil {
		t.Fatalf("%d addresses must be refused", limit+1)
	}
}

// The cap falls back to the compiled default rather than disappearing when no
// config is loaded.
func TestMaxFilterAddresses_FailsClosed(t *testing.T) {
	if got := maxFilterAddresses(); got <= 0 {
		t.Fatalf("cap must never be unlimited, got %d", got)
	}
}

// A single address string (what most client libraries send) is unaffected.
func TestToFilterQuery_SingleAddressStringUnchanged(t *testing.T) {
	q, err := toFilterQuery(map[string]any{"address": "0x00000000000000000000000000000000000000a1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(q.Addresses) != 1 {
		t.Fatalf("want 1 address, got %d", len(q.Addresses))
	}
}
