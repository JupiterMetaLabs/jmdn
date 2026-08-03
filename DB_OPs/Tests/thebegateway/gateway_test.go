package thebegateway_test

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"gossipnode/DB_OPs/thebegateway"
)

func newGateway(app *spyAppender, kv *spyKV, c *spyCache, out *spyOutbox) thebegateway.ThebeGateway {
	return thebegateway.NewThebeGateway(app, kv, c, out)
}

// TestWriteBlock covers the 2PC write path for blocks.
func TestWriteBlock(t *testing.T) {
	t.Run("success_appends_and_caches", func(t *testing.T) {
		app := &spyAppender{}
		c := newSpyCache()
		out := &spyOutbox{}
		gw := newGateway(app, &spyKV{}, c, out)

		block := &thebegateway.BlockRecord{BlockNumber: 42, BlockHash: "0xabc"}
		if err := gw.WriteBlock(context.Background(), block); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if app.callCount() != 1 {
			t.Errorf("want 1 append call, got %d", app.callCount())
		}
		if c.setCallCount() != 1 {
			t.Errorf("want 1 cache.Set call, got %d", c.setCallCount())
		}
		wantKey := thebegateway.BlockKey(42)
		if got := c.lastSetKey(); got != wantKey {
			t.Errorf("cache key: want %q, got %q", wantKey, got)
		}
	})

	t.Run("appender_error_enqueues_to_outbox", func(t *testing.T) {
		app := &spyAppender{err: errAppend}
		c := newSpyCache()
		out := &spyOutbox{}
		gw := newGateway(app, &spyKV{}, c, out)

		block := &thebegateway.BlockRecord{BlockNumber: 1}
		err := gw.WriteBlock(context.Background(), block)
		if err == nil {
			t.Fatal("expected error")
		}
		if out.enqueueCalls != 1 {
			t.Errorf("want 1 outbox.Enqueue, got %d", out.enqueueCalls)
		}
		if c.setCallCount() != 0 {
			t.Errorf("cache.Set must not be called on appender failure")
		}
	})

	t.Run("appender_error_and_outbox_error_combined_message", func(t *testing.T) {
		app := &spyAppender{err: errAppend}
		c := newSpyCache()
		out := &spyOutbox{enqueueErr: errOutbox}
		gw := newGateway(app, &spyKV{}, c, out)

		err := gw.WriteBlock(context.Background(), &thebegateway.BlockRecord{})
		if err == nil {
			t.Fatal("expected error")
		}
		msg := err.Error()
		if !strings.Contains(msg, errAppend.Error()) {
			t.Errorf("error should mention append error; got: %s", msg)
		}
		if !strings.Contains(msg, errOutbox.Error()) {
			t.Errorf("error should mention outbox error; got: %s", msg)
		}
	})

	t.Run("nil_cache_does_not_panic", func(t *testing.T) {
		app := &spyAppender{}
		out := &spyOutbox{}
		// nil cache accepted by NewThebeGateway
		gw := thebegateway.NewThebeGateway(app, &spyKV{}, nil, out)

		if err := gw.WriteBlock(context.Background(), &thebegateway.BlockRecord{BlockNumber: 99}); err != nil {
			t.Fatalf("unexpected error with nil cache: %v", err)
		}
		if app.callCount() != 1 {
			t.Errorf("want 1 append, got %d", app.callCount())
		}
	})
}

// TestWriteAccount checks namespace and cache key correctness.
func TestWriteAccount(t *testing.T) {
	app := &spyAppender{}
	c := newSpyCache()
	out := &spyOutbox{}
	gw := newGateway(app, &spyKV{}, c, out)

	acc := &thebegateway.AccountRecord{Address: "0xdeadbeef"}
	if err := gw.WriteAccount(context.Background(), acc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call, ok := app.lastCall()
	if !ok {
		t.Fatal("no append call recorded")
	}
	if call.ns != string(thebegateway.NamespaceAccount) {
		t.Errorf("namespace: want %q, got %q", thebegateway.NamespaceAccount, call.ns)
	}
	wantKey := thebegateway.AccountKey("0xdeadbeef")
	if got := c.lastSetKey(); got != wantKey {
		t.Errorf("cache key: want %q, got %q", wantKey, got)
	}
}

// TestWriteContractCode verifies PutWorm path (not 2PC Append).
func TestWriteContractCode(t *testing.T) {
	app := &spyAppender{}
	kv := &spyKV{}
	out := &spyOutbox{}
	gw := newGateway(app, kv, nil, out)

	rec := &thebegateway.ContractCodeRecord{Address: "0xcontract", Code: []byte{0x60, 0x80}}
	if err := gw.WriteContractCode(context.Background(), rec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if app.callCount() != 0 {
		t.Errorf("WriteContractCode must not call Append (KV path), got %d calls", app.callCount())
	}

	last, ok := kv.lastCall()
	if !ok {
		t.Fatal("no KV call recorded")
	}
	if last.op != "worm" {
		t.Errorf("expected PutWorm, got op=%q", last.op)
	}
	if !strings.HasPrefix(string(last.key), "contract:code:") {
		t.Errorf("key prefix: want contract:code:, got %q", string(last.key))
	}
}

// TestWriteContractNonce verifies raw big-endian uint64 encoding via PutDerived.
func TestWriteContractNonce(t *testing.T) {
	kv := &spyKV{}
	gw := newGateway(&spyAppender{}, kv, nil, &spyOutbox{})

	const wantNonce uint64 = 7
	rec := &thebegateway.ContractNonceRecord{Address: "0xaddr", Nonce: wantNonce}
	if err := gw.WriteContractNonce(context.Background(), rec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	last, ok := kv.lastCall()
	if !ok {
		t.Fatal("no KV call")
	}
	if last.op != "derived" {
		t.Errorf("expected PutDerived, got %q", last.op)
	}
	if len(last.value) != 8 {
		t.Fatalf("nonce value: want 8 bytes, got %d", len(last.value))
	}
	// raw big-endian — NOT JSON (JSON encodes 7 as ASCII "7", not 8 bytes)
	got := binary.BigEndian.Uint64(last.value)
	if got != wantNonce {
		t.Errorf("decoded nonce: want %d, got %d", wantNonce, got)
	}
}

// TestWriteContractStorage checks binary key construction and error cases.
func TestWriteContractStorage(t *testing.T) {
	validAddr := "0x" + strings.Repeat("ab", 20) // 40 hex chars = 20 bytes
	validSlot := "0x" + strings.Repeat("cd", 32) // 64 hex chars = 32 bytes

	t.Run("invalid_address_hex_returns_error", func(t *testing.T) {
		kv := &spyKV{}
		gw := newGateway(&spyAppender{}, kv, nil, &spyOutbox{})
		rec := &thebegateway.ContractStorageRecord{Address: "notHex!", Slot: validSlot}
		if err := gw.WriteContractStorage(context.Background(), rec); err == nil {
			t.Fatal("expected error for invalid address hex")
		}
	})

	t.Run("invalid_slot_hex_returns_error", func(t *testing.T) {
		kv := &spyKV{}
		gw := newGateway(&spyAppender{}, kv, nil, &spyOutbox{})
		rec := &thebegateway.ContractStorageRecord{Address: validAddr, Slot: "notHex!"}
		if err := gw.WriteContractStorage(context.Background(), rec); err == nil {
			t.Fatal("expected error for invalid slot hex")
		}
	})

	t.Run("valid_key_has_correct_length", func(t *testing.T) {
		kv := &spyKV{}
		gw := newGateway(&spyAppender{}, kv, nil, &spyOutbox{})
		rec := &thebegateway.ContractStorageRecord{
			Address: validAddr,
			Slot:    validSlot,
		}
		if err := gw.WriteContractStorage(context.Background(), rec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		last, ok := kv.lastCall()
		if !ok {
			t.Fatal("no KV call")
		}
		if last.op != "derived" {
			t.Errorf("expected PutDerived, got %q", last.op)
		}
		const prefix = "contract:storage:"
		wantLen := len(prefix) + 20 + 32
		if len(last.key) != wantLen {
			t.Errorf("key length: want %d, got %d", wantLen, len(last.key))
		}
	})
}

// TestWriteContractReceipt verifies receipt goes via 2PC (Append), NOT PutWorm.
func TestWriteContractReceipt(t *testing.T) {
	app := &spyAppender{}
	kv := &spyKV{}
	gw := newGateway(app, kv, newSpyCache(), &spyOutbox{})

	rec := &thebegateway.ContractReceiptRecord{TxHash: "0xreceipt", BlockNumber: 1}
	if err := gw.WriteContractReceipt(context.Background(), rec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if app.callCount() != 1 {
		t.Errorf("want 1 Append call, got %d", app.callCount())
	}
	for _, c := range kv.calls {
		if c.op == "worm" {
			t.Errorf("WriteContractReceipt must not call PutWorm")
		}
	}
	call, _ := app.lastCall()
	if call.ns != string(thebegateway.NamespaceContractReceipt) {
		t.Errorf("namespace: want %q, got %q", thebegateway.NamespaceContractReceipt, call.ns)
	}
}
