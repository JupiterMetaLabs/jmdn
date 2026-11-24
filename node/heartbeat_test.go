package node

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"gossipnode/config"
	"gossipnode/config/settings"

	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
)

// TestMain loads settings: the node logger is built from settings.Get().
func TestMain(m *testing.M) {
	if _, err := settings.Load(); err != nil {
		panic("node test: load settings: " + err.Error())
	}
	code := m.Run()
	_ = os.Remove("config/bls.json")
	_ = os.Remove("config/peer.json")
	_ = os.Remove("config")
	os.Exit(code)
}

// heartbeatPair returns a client host and a server host whose heartbeat
// handler is the real NodeManager.handleHeartbeat.
func heartbeatPair(t *testing.T) func(payload string) (string, error) {
	t.Helper()
	mn := mocknet.New()
	t.Cleanup(func() { _ = mn.Close() })
	client, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	server, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	if err := mn.LinkAll(); err != nil {
		t.Fatal(err)
	}
	if err := mn.ConnectAllButSelf(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	nm := &NodeManager{host: server, trackedPeers: map[peer.ID]*ManagedPeer{}, ctx: ctx}
	server.SetStreamHandler(config.HeartbeatProtocol, nm.handleHeartbeat)

	send := func(payload string) (string, error) {
		s, err := client.NewStream(ctx, server.ID(), config.HeartbeatProtocol)
		if err != nil {
			return "", err
		}
		defer s.Close()
		if _, err := s.Write([]byte(payload)); err != nil {
			return "", err
		}
		_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 16)
		n, err := s.Read(buf)
		return string(buf[:n]), err
	}
	return send
}

// A valid heartbeat is answered with OK.
func TestHandleHeartbeat_ValidGetsOK(t *testing.T) {
	send := heartbeatPair(t)
	resp, err := send("HEARTBEAT\n")
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.Contains(resp, "OK") {
		t.Fatalf("want OK, got %q", resp)
	}
}

// Pins the mechanism behind the production log line
// "Failed to read heartbeat response ... error: EOF": the handler closes the
// stream WITHOUT a reply when the payload is not HEARTBEAT, and the sender's
// Read returns io.EOF.
func TestHandleHeartbeat_InvalidPayloadIsEOFAtSender(t *testing.T) {
	send := heartbeatPair(t)
	_, err := send("garbage\n")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF at the sender for an invalid payload, got %v", err)
	}
}

func TestAllowHeartbeatWarn_RateLimitsPerPeer(t *testing.T) {
	heartbeatWarnMu.Lock()
	heartbeatWarnLast = make(map[peer.ID]time.Time)
	heartbeatWarnMu.Unlock()

	a, b := peer.ID("peer-a"), peer.ID("peer-b")
	t0 := time.Unix(1_800_000_000, 0)
	if !allowHeartbeatWarn(a, t0) {
		t.Fatal("first warn for a must be allowed")
	}
	if allowHeartbeatWarn(a, t0.Add(30*time.Second)) {
		t.Fatal("second warn for a within the interval must be suppressed")
	}
	if !allowHeartbeatWarn(b, t0.Add(30*time.Second)) {
		t.Fatal("other peers are limited independently")
	}
	if !allowHeartbeatWarn(a, t0.Add(heartbeatWarnInterval)) {
		t.Fatal("warn for a must be allowed again after the interval")
	}
	heartbeatWarnMu.Lock()
	n := len(heartbeatWarnLast)
	heartbeatWarnMu.Unlock()
	if n > 2 {
		t.Fatalf("stale entries not pruned: %d", n)
	}
}
