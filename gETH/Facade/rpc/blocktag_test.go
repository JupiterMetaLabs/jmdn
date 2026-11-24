package rpc

import (
	"context"
	"testing"
)

// A hex block tag must be a uint64 height. big.Int accepts a sign, so
// "0x-40000000" used to parse as a negative height that eth_feeHistory turned
// into a billion-entry reply; a tag that does not parse used to come back as a
// number instead of an error. Hex tags never reach the service, so nil is fine.
func TestParseBlockTag_RejectsInvalidNumbers(t *testing.T) {
	ctx := context.Background()
	for _, tag := range []string{
		"0x-1",
		"0x-40000000",
		"0x-8000000000000000",
		"0xzz",
		"0x",
		"0x10000000000000000", // 2^64, one past the largest height
	} {
		if n, err := parseBlockTag(ctx, nil, tag); err == nil {
			t.Errorf("tag %q: want an error, got %v", tag, n)
		}
	}

	for tag, want := range map[string]uint64{
		"0x0":                0,
		"0x10":               16,
		"0xffffffffffffffff": ^uint64(0),
	} {
		n, err := parseBlockTag(ctx, nil, tag)
		if err != nil {
			t.Fatalf("tag %q: %v", tag, err)
		}
		if n.Uint64() != want {
			t.Errorf("tag %q = %v, want %d", tag, n, want)
		}
	}
}
