package rpc

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// slowReaderService serves newHeads from a channel that only its feeder
// goroutine sends on and closes, so a stop never races a send.
type slowReaderService struct {
	Service.Service
	stops   atomic.Int32
	stopped chan struct{}
	ch      chan *Types.Block
}

func (f *slowReaderService) SubscribeNewHeads(ctx context.Context) (<-chan *Types.Block, func(), error) {
	var once sync.Once
	return f.ch, func() {
		once.Do(func() {
			f.stops.Add(1)
			close(f.stopped)
		})
	}, nil
}

func (f *slowReaderService) GetChainIDValue() *big.Int { return big.NewInt(1) }

// A client that stops reading is disconnected once a write blocks past the
// write timeout, and its subscriptions are stopped.
func TestWS_ClientThatStopsReadingIsDisconnected(t *testing.T) {
	be := &slowReaderService{stopped: make(chan struct{}), ch: make(chan *Types.Block, 100)}
	s := NewWSServer(NewHandlers(be), be)
	s.writeTimeout = 200 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(s.handleWS))
	defer srv.Close()

	c := dialWS(t, srv)
	defer c.Close()
	_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "eth_subscribe", "params": []any{"newHeads"}})
	var first map[string]any
	if err := c.ReadJSON(&first); err != nil {
		t.Fatal(err)
	}

	// The client reads nothing from here on; the server keeps pushing heads
	// until its writes block and time out.
	go func() {
		defer close(be.ch)
		for i := 0; ; i++ {
			select {
			case <-be.stopped:
				return
			case be.ch <- &Types.Block{Header: &Types.BlockHeader{Number: uint64(i)}}:
			}
		}
	}()

	select {
	case <-be.stopped:
	case <-time.After(20 * time.Second):
		t.Fatal("a client that stopped reading was not disconnected")
	}
}

// endingLogsService delivers one log and then ends the subscription itself,
// as the node does for a client that stopped reading.
type endingLogsService struct {
	fakeWSService
}

func (f *endingLogsService) SubscribeLogs(ctx context.Context, q *Types.FilterQuery) (<-chan Types.Log, func(), error) {
	ch := make(chan Types.Log, 1)
	ch <- Types.Log{BlockNumber: 1}
	close(ch)
	return ch, func() {}, nil
}

// A subscription the node ends is reported to the client with a close frame,
// not left open and silent.
func TestWS_NodeEndedSubscriptionClosesConnection(t *testing.T) {
	be := &endingLogsService{}
	s := NewWSServer(NewHandlers(be), be)
	srv := httptest.NewServer(http.HandlerFunc(s.handleWS))
	defer srv.Close()

	c := dialWS(t, srv)
	defer c.Close()
	_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "eth_subscribe", "params": []any{"logs", map[string]any{}}})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var m map[string]any
		err := c.ReadJSON(&m)
		if err == nil {
			continue // the subscribe response and the one log
		}
		if !websocket.IsCloseError(err, websocket.CloseTryAgainLater) {
			t.Fatalf("want a close frame with code %d, got %v", websocket.CloseTryAgainLater, err)
		}
		return
	}
}

// eth_unsubscribe ends a subscription without closing the connection.
func TestWS_UnsubscribeKeepsConnection(t *testing.T) {
	be := &fakeWSService{}
	s := NewWSServer(NewHandlers(be), be)
	srv := httptest.NewServer(http.HandlerFunc(s.handleWS))
	defer srv.Close()

	c := dialWS(t, srv)
	defer c.Close()
	_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "eth_subscribe", "params": []any{"newHeads"}})
	var sub map[string]any
	if err := c.ReadJSON(&sub); err != nil {
		t.Fatal(err)
	}
	_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "eth_unsubscribe", "params": []any{sub["result"]}})
	var unsub map[string]any
	if err := c.ReadJSON(&unsub); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the forwarder see its channel close
	_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "eth_unknownMethod"})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp map[string]any
	if err := c.ReadJSON(&resp); err != nil {
		t.Fatalf("connection closed after eth_unsubscribe: %v", err)
	}
}
