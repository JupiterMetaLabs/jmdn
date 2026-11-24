package thebegateway_test

import (
	"context"
	"encoding/json"
	"testing"

	"gossipnode/DB_OPs/thebegateway"
)

// DeleteAccount appends one account-namespace record flagged Deleted and drops
// the account's cache entry instead of caching anything.
func TestDeleteAccount_AppendsDeleteAndDropsCache(t *testing.T) {
	app := &spyAppender{}
	c := newSpyCache()
	out := &spyOutbox{}
	gw := newGateway(app, &spyKV{}, c, out)

	if err := gw.DeleteAccount(context.Background(), "0xdead"); err != nil {
		t.Fatal(err)
	}
	call, ok := app.lastCall()
	if !ok || call.ns != string(thebegateway.NamespaceAccount) {
		t.Fatalf("want one account-namespace append, got %+v (ok=%v)", call, ok)
	}
	var rec thebegateway.AccountRecord
	if err := json.Unmarshal(call.value, &rec); err != nil || !rec.Deleted || rec.Address != "0xdead" {
		t.Fatalf("appended record %+v (%v), want a Deleted record for 0xdead", rec, err)
	}
	if c.setCallCount() != 0 {
		t.Fatal("a delete must not populate the cache")
	}
	found := false
	for _, k := range c.deleted {
		found = found || k == thebegateway.AccountKey("0xdead")
	}
	if !found {
		t.Fatalf("cache keys deleted: %v, want the account key", c.deleted)
	}
}

// A delete whose append fails is returned and never enqueued: replayed late it
// could remove an account a later block created.
func TestDeleteAccount_AppendFailureIsNotEnqueued(t *testing.T) {
	app := &spyAppender{err: errAppend}
	out := &spyOutbox{}
	gw := newGateway(app, &spyKV{}, newSpyCache(), out)
	if err := gw.DeleteAccount(context.Background(), "0xdead"); err == nil {
		t.Fatal("want the append error")
	}
	if out.enqueueCount() != 0 {
		t.Fatal("a failed delete must not be enqueued to the outbox")
	}
}

// Ordinary account records still omit the flag, so their bytes are unchanged.
func TestAccountRecord_DeletedOmittedByDefault(t *testing.T) {
	b, err := json.Marshal(&thebegateway.AccountRecord{Address: "0x1"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["deleted"]; ok {
		t.Fatalf("an ordinary record carries the deleted field: %s", b)
	}
}

// An outbox entry carrying a Deleted account record is acknowledged without a
// gateway write: DeleteAccount never enqueues, and replaying a delete late
// could remove a newer account.
func TestDrainBatch_DeletedAccountIsDropped(t *testing.T) {
	payload, _ := json.Marshal(thebegateway.AccountRecord{Address: "0xdead", Deleted: true})
	store := &spyOutbox{nextEntries: []thebegateway.OutboxEntry{
		{ID: 303, Namespace: thebegateway.NamespaceAccount, Payload: payload},
	}}
	gw := &spyGateway{}
	drainOnce(store, gw)
	gw.mu.Lock()
	n := len(gw.calls)
	gw.mu.Unlock()
	if n != 0 || store.ackCount() != 1 {
		t.Fatalf("want the entry acked with no gateway write; writes=%d acks=%d", n, store.ackCount())
	}
}

// A write under WithoutOutbox whose append fails is returned and never
// enqueued; the same write without it is enqueued for retry, as before.
func TestWrite_WithoutOutboxIsNeverEnqueued(t *testing.T) {
	app := &spyAppender{err: errAppend}
	out := &spyOutbox{}
	gw := newGateway(app, &spyKV{}, newSpyCache(), out)
	ctx := thebegateway.WithoutOutbox(context.Background())
	if err := gw.WriteAccount(ctx, &thebegateway.AccountRecord{Address: "0xbeef"}); err == nil {
		t.Fatal("want the append error")
	}
	if out.enqueueCount() != 0 {
		t.Fatal("a write under WithoutOutbox was enqueued")
	}
	if err := gw.WriteAccount(context.Background(), &thebegateway.AccountRecord{Address: "0xbeef"}); err == nil {
		t.Fatal("want the append error")
	}
	if out.enqueueCount() != 1 {
		t.Fatal("an ordinary failed write must still be enqueued")
	}
}

// DeleteAccount drops the account's cache entry under both address forms.
func TestDeleteAccount_DropsBothCacheKeyForms(t *testing.T) {
	c := newSpyCache()
	gw := newGateway(&spyAppender{}, &spyKV{}, c, &spyOutbox{})
	if err := gw.DeleteAccount(context.Background(), "0xAbC"); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{thebegateway.AccountKey("0xAbC"): true, thebegateway.AccountKey("0xabc"): true}
	for _, k := range c.deleted {
		delete(want, k)
	}
	if len(want) != 0 {
		t.Fatalf("cache keys not dropped: %v (dropped %v)", want, c.deleted)
	}
}
