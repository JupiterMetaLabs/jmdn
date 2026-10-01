package messaging

// D-48 (remainder): regression tests for the per-peer rate limit and the
// global concurrency cap added to HandleVDFProofRequestStream. The gate
// test (d48_vdf_beacon_gate_test.go) already covers the vdfBeaconInstalled
// boot-time gate; these cover the two runtime bounds added alongside it.

import (
	"bufio"
	"testing"
	"time"

	"gossipnode/config"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

// resetVDFPullLimits clears both package-level bounds so tests don't leak
// state into each other — the LRU cache and the concurrency semaphore are
// both process-lifetime singletons, same reasoning as resetEquivocation and
// the other package-level test resets in this file's siblings.
func resetVDFPullLimits(t *testing.T) {
	t.Helper()
	vdfPullRateLimiters.Purge()
	for len(vdfPullConcurrency) > 0 {
		<-vdfPullConcurrency
	}
}

// TestVDFPullRateLimiterFor_IndependentPerPeer proves two different peers
// never share a budget — one peer exhausting its own allowance must not
// affect a different peer's very first request.
func TestVDFPullRateLimiterFor_IndependentPerPeer(t *testing.T) {
	resetVDFPullLimits(t)

	peerA := peer.ID("peer-a")
	peerB := peer.ID("peer-b")

	limA := vdfPullRateLimiterFor(peerA)
	for i := 0; i < vdfPullRateBurst; i++ {
		if !limA.Allow() {
			t.Fatalf("peer A should still have budget at request %d of its burst %d", i+1, vdfPullRateBurst)
		}
	}
	if limA.Allow() {
		t.Fatal("peer A should be over its burst budget and rejected")
	}

	limB := vdfPullRateLimiterFor(peerB)
	if !limB.Allow() {
		t.Fatal("peer B's first request was rejected by peer A's exhausted budget — limiters are not independent per peer")
	}
}

// TestVDFPullRateLimiterFor_SamePeerReusesTheSameLimiter proves the LRU
// cache is actually doing its job of returning the SAME limiter object for
// repeated lookups of one peer, not silently minting a fresh (full-budget)
// one on every call — which would make the rate limit a no-op.
func TestVDFPullRateLimiterFor_SamePeerReusesTheSameLimiter(t *testing.T) {
	resetVDFPullLimits(t)

	p := peer.ID("peer-repeat")
	first := vdfPullRateLimiterFor(p)
	second := vdfPullRateLimiterFor(p)
	if first != second {
		t.Fatal("vdfPullRateLimiterFor returned a different *rate.Limiter for the same peer on a second call — the rate limit would never actually bind")
	}
}

// TestHandleVDFProofRequestStream_DropsRequestOverRateLimit is the decisive
// stream-level proof: a peer that has already exhausted its budget (driven
// directly, not by racing real requests over the wire, to keep this
// deterministic) gets the stream closed with zero response bytes on its
// NEXT request. Beacon-installed state is irrelevant here — the rate limit
// is checked before that branch — so this test does not touch it.
func TestHandleVDFProofRequestStream_DropsRequestOverRateLimit(t *testing.T) {
	resetVDFPullLimits(t)

	server, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New server: %v", err)
	}
	defer server.Close()
	client, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New client: %v", err)
	}
	defer client.Close()

	server.SetStreamHandler(config.VDFProofRequestProtocol, HandleVDFProofRequestStream)
	client.Peerstore().AddAddrs(server.ID(), server.Addrs(), time.Hour)
	if err := client.Connect(t.Context(), peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Drive this client's own limiter to empty directly — deterministic,
	// unlike racing vdfPullRateBurst real requests over the wire.
	lim := vdfPullRateLimiterFor(client.ID())
	for lim.Allow() {
	}

	s, err := client.NewStream(t.Context(), server.ID(), config.VDFProofRequestProtocol)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer s.Close()

	if _, err := s.Write([]byte(`{"epoch":42}` + "\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = s.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(s).ReadString('\n')
	if err == nil {
		t.Fatalf("expected a rate-limited server to close without answering, but got a response line: %q", line)
	}
	if line != "" {
		t.Fatalf("expected zero bytes from a rate-limited responder, got %d bytes: %q", len(line), line)
	}
}

// TestHandleVDFProofRequestStream_DropsRequestOverConcurrencyCap fills the
// global concurrency semaphore directly, then proves a new request is
// dropped rather than queued or served — and that releasing one slot lets
// the next request through, confirming the cap is a moving window, not a
// one-time lockout.
func TestHandleVDFProofRequestStream_DropsRequestOverConcurrencyCap(t *testing.T) {
	resetVDFPullLimits(t)

	server, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New server: %v", err)
	}
	defer server.Close()
	client, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New client: %v", err)
	}
	defer client.Close()

	server.SetStreamHandler(config.VDFProofRequestProtocol, HandleVDFProofRequestStream)
	client.Peerstore().AddAddrs(server.ID(), server.Addrs(), time.Hour)
	if err := client.Connect(t.Context(), peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	for i := 0; i < vdfPullMaxConcurrent; i++ {
		vdfPullConcurrency <- struct{}{}
	}
	t.Cleanup(func() {
		for len(vdfPullConcurrency) > 0 {
			<-vdfPullConcurrency
		}
	})

	s, err := client.NewStream(t.Context(), server.ID(), config.VDFProofRequestProtocol)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer s.Close()

	if _, err := s.Write([]byte(`{"epoch":42}` + "\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = s.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(s).ReadString('\n')
	if err == nil {
		t.Fatalf("expected a server at max concurrency to close without answering, but got a response line: %q", line)
	}
	if line != "" {
		t.Fatalf("expected zero bytes from a server at max concurrency, got %d bytes: %q", len(line), line)
	}

	// Free one slot and confirm the NEXT request goes through — the cap
	// must be a live gate, not something that permanently wedges once hit.
	<-vdfPullConcurrency

	s2, err := client.NewStream(t.Context(), server.ID(), config.VDFProofRequestProtocol)
	if err != nil {
		t.Fatalf("open second stream: %v", err)
	}
	defer s2.Close()

	if _, err := s2.Write([]byte(`{"epoch":42}` + "\n")); err != nil {
		t.Fatalf("write second request: %v", err)
	}

	_ = s2.SetReadDeadline(time.Now().Add(2 * time.Second))
	line2, err := bufio.NewReader(s2).ReadString('\n')
	if err != nil {
		t.Fatalf("expected a server with a freed slot to answer, got error: %v", err)
	}
	if line2 == "" {
		t.Fatal("expected a non-empty response line once a concurrency slot freed up")
	}
}
