package DB_OPs

import (
	"context"
	"testing"

	"gossipnode/DB_OPs/store"
)

type fakeTaskHandle struct{ store.ThebeHandle }

func (fakeTaskHandle) Close() error { return nil }

// A task connection routes getHandle to that task's handle; an unregistered
// task yields nil, which falls back to the process-wide handle.
func TestTaskConn_RoutesToTaskHandle(t *testing.T) {
	globalH := &fakeTaskHandle{}
	syncH := &fakeTaskHandle{}
	SetGlobalHandle(globalH)
	defer SetGlobalHandle(nil)
	SetTaskHandle(TaskSync, syncH)
	defer SetTaskHandle(TaskSync, nil)

	h, err := getHandle(TaskConn(TaskSync))
	if err != nil || h != store.ThebeHandle(syncH) {
		t.Fatalf("sync conn must route to the sync handle: h=%p err=%v", h, err)
	}
	if TaskConn(TaskRead) != nil {
		t.Fatal("unregistered task must return a nil conn")
	}
	h, err = getHandle(TaskConn(TaskRead))
	if err != nil || h != store.ThebeHandle(globalH) {
		t.Fatalf("unregistered task must fall back to the process handle: h=%p err=%v", h, err)
	}
	SetTaskHandle(TaskSync, nil)
	if TaskConn(TaskSync) != nil {
		t.Fatal("SetTaskHandle(nil) must unregister")
	}
}

// A zero budget means UNLIMITED in database/sql; it must be rejected, not
// silently remove the cap.
func TestOpenTaskDB_RejectsZeroBudget(t *testing.T) {
	if _, err := OpenTaskDB(context.Background(), TaskSync, "postgres://invalid", TaskPoolOptions{MaxOpen: 0}); err == nil {
		t.Fatal("MaxOpen 0 must be rejected")
	}
}

func TestTaskPoolOptions_Normalized(t *testing.T) {
	o := TaskPoolOptions{MaxOpen: 5}.normalized()
	if o.MaxIdle != 3 || o.ConnMaxLifetime <= 0 || o.ConnMaxIdleTime <= 0 {
		t.Fatalf("defaults not applied: %+v", o)
	}
	if o := (TaskPoolOptions{MaxOpen: 4, MaxIdle: 9}).normalized(); o.MaxIdle != 2 {
		t.Fatalf("MaxIdle above MaxOpen must be clamped, got %d", o.MaxIdle)
	}
}
