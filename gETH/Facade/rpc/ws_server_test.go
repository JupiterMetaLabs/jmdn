package rpc

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/Service/Types"
)

// fakeWSService serves newHeads from a channel it controls and records stops.
type fakeWSService struct {
	Service.Service
	stops atomic.Int32
	ch    chan *Types.Block
}

func (f *fakeWSService) SubscribeNewHeads(ctx context.Context) (<-chan *Types.Block, func(), error) {
	f.ch = make(chan *Types.Block, 100)
	var stopped atomic.Bool
	return f.ch, func() {
		if stopped.CompareAndSwap(false, true) {
			f.stops.Add(1)
			close(f.ch)
		}
	}, nil
}

func (f *fakeWSService) GetChainIDValue() *big.Int { return big.NewInt(1) }

func dialWS(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Closing the connection must stop every subscription it opened.
func TestWS_DisconnectStopsSubscriptions(t *testing.T) {
	be := &fakeWSService{}
	s := NewWSServer(NewHandlers(be), be)
	srv := httptest.NewServer(http.HandlerFunc(s.handleWS))
	defer srv.Close()

	c := dialWS(t, srv)
	if err := c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "eth_subscribe", "params": []any{"newHeads"}}); err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	if err := c.ReadJSON(&resp); err != nil {
		t.Fatal(err)
	}
	id, _ := resp["result"].(string)
	if len(id) != 34 || !strings.HasPrefix(id, "0x") {
		t.Fatalf("subscription id %q is not a 16-byte hex id", id)
	}
	_ = c.Close()

	deadline := time.Now().Add(2 * time.Second)
	for be.stops.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if be.stops.Load() != 1 {
		t.Fatalf("subscription not stopped on disconnect (stops=%d)", be.stops.Load())
	}
}

// Subscription pushes and request responses write concurrently to one
// connection; with -race this catches unsynchronized writes.
func TestWS_ConcurrentWritesAreSerialized(t *testing.T) {
	be := &fakeWSService{}
	s := NewWSServer(NewHandlers(be), be)
	srv := httptest.NewServer(http.HandlerFunc(s.handleWS))
	defer srv.Close()

	c := dialWS(t, srv)
	defer c.Close()
	_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "eth_subscribe", "params": []any{"newHeads"}})
	var first map[string]any
	_ = c.ReadJSON(&first)

	go func() {
		for i := 0; i < 50; i++ {
			be.ch <- &Types.Block{Header: &Types.BlockHeader{Number: uint64(i)}}
		}
	}()
	for i := 0; i < 50; i++ {
		_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": i + 2, "method": "eth_unknownMethod"})
	}
	got := 0
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for got < 100 {
		var m map[string]any
		if err := c.ReadJSON(&m); err != nil {
			break
		}
		got++
	}
	if got < 100 {
		t.Fatalf("received %d of 100 messages; a write was lost or the connection broke", got)
	}
}
