package saferun

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"gossipnode/metrics"
)

// A panicking stream handler must not kill the process: the panic is counted,
// the stream is reset, and the host keeps serving.
func TestStream_RecoversAndResets(t *testing.T) {
	a, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	const proto = "/test/panic/1.0.0"
	before := testutil.ToFloat64(metrics.HandlerPanicsCounter.WithLabelValues(proto))
	b.SetStreamHandler(proto, Stream(proto, func(s network.Stream) {
		var m map[string]int
		m["boom"] = 1 // nil-map write: panics
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Connect(ctx, peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // second stream proves the host is still alive
		s, err := a.NewStream(ctx, b.ID(), proto)
		if err != nil {
			t.Fatalf("NewStream %d: %v", i, err)
		}
		_, _ = s.Write([]byte("x\n"))
		buf := make([]byte, 1)
		_ = s.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := s.Read(buf); err == nil {
			t.Fatal("expected the stream to be reset")
		}
		_ = s.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for testutil.ToFloat64(metrics.HandlerPanicsCounter.WithLabelValues(proto))-before < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := testutil.ToFloat64(metrics.HandlerPanicsCounter.WithLabelValues(proto)) - before; got != 2 {
		t.Fatalf("panics counted = %v, want 2", got)
	}
}

func TestRecoverInto_CallsBack(t *testing.T) {
	called := false
	func() {
		defer RecoverInto("test.cb", func(any) { called = true })
		panic("x")
	}()
	if !called {
		t.Fatal("callback not invoked")
	}
}
