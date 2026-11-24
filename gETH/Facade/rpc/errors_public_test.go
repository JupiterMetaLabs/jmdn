package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPublicErrorMessage(t *testing.T) {
	ctx := context.Background()
	if got := publicErrorMessage(ctx, errors.New("GetBlock: sql: no rows in result set")); got != "not found" {
		t.Errorf("missing row = %q, want not found", got)
	}
	for _, msg := range []string{"nonce too low", "execution reverted: insufficient allowance", "transaction not found"} {
		if got := publicErrorMessage(ctx, errors.New(msg)); got != msg {
			t.Errorf("request error %q altered to %q", msg, got)
		}
	}
	for _, msg := range []string{
		`pq: password authentication failed for user "jmdn"`,
		"dial tcp 10.0.3.7:5430: connect: connection refused",
		"open /opt/jmdn/storage/thebe-kv/MANIFEST: permission denied",
	} {
		got := publicErrorMessage(ctx, errors.New(msg))
		if !strings.HasPrefix(got, "internal error (ref ") {
			t.Errorf("internal error %q leaked as %q", msg, got)
		}
	}
}
