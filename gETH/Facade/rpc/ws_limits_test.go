package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConnLimiter(t *testing.T) {
	l := newConnLimiter(3, 2)
	if !l.acquire("a") || !l.acquire("a") {
		t.Fatal("first two per-IP slots must be granted")
	}
	if l.acquire("a") {
		t.Fatal("per-IP cap exceeded")
	}
	if !l.acquire("b") {
		t.Fatal("other IP must get a slot")
	}
	if l.acquire("c") {
		t.Fatal("total cap exceeded")
	}
	l.release("a")
	if !l.acquire("c") {
		t.Fatal("released slot must be reusable")
	}
	l.release("a")
	l.release("b")
	l.release("c")
	if l.total != 0 || len(l.perIP) != 0 {
		t.Fatalf("leaked state: total=%d perIP=%v", l.total, l.perIP)
	}
}

// Connections beyond the per-IP cap are refused before the upgrade, and a
// closed connection frees its slot.
func TestWS_PerIPConnectionCap(t *testing.T) {
	be := &fakeWSService{}
	s := NewWSServer(NewHandlers(be), be)
	s.conns = newConnLimiter(10, 1)
	srv := httptest.NewServer(http.HandlerFunc(s.handleWS))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	c1, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second connection from same IP: err=%v resp=%v, want 503", err, resp)
	}
	_ = c1.Close()

	// The handler releases the slot when its read loop exits.
	var c3 *websocket.Conn
	for i := 0; i < 100; i++ {
		if c3, _, err = websocket.DefaultDialer.Dial(url, nil); err == nil {
			break
		}
		<-timeAfterMs(10)
	}
	if err != nil {
		t.Fatalf("slot not released after close: %v", err)
	}
	_ = c3.Close()
}

func timeAfterMs(ms int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		<-time.After(time.Duration(ms) * time.Millisecond)
	}()
	return ch
}
