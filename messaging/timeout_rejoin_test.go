package messaging

// Tests for the timeout-certificate rejoin/catch-up RPC (timeout_rejoin.go),
// closing the "P2P rejoin transport ... remains a TODO" gap in M0/§7.1c.
//
// Required coverage:
//  1. server answers "not found" for a height it has never accepted a
//     certificate for.
//  2. a real certificate is served and the CLIENT independently verifies
//     and accepts it (its own PeriodStore actually advances) — over a REAL
//     libp2p network, not an in-process stand-in.
//  3. a certificate signed by keys outside the client's own eligible pool
//     (unverifiable/forged) is rejected — the RPC layer adds no new trust.
//  4. an unresponsive first peer does not block a second, good peer from
//     being tried.
//
// NOTE on why the "server" side in tests 2-4 below is a hand-built stream
// handler rather than the real HandleTimeoutCertRejoinStream: that function
// reads the package-level defaultTimeoutVoteCollector singleton (same
// process-wide-by-design pattern flagged in timeout_gossip_test.go's own
// file header), so "server already has it, client doesn't yet" cannot be
// modeled by two divergent copies of one global inside a single test
// process. Serving a precomputed certificate from a closed-over local
// variable isolates what these tests actually need to prove — the CLIENT
// side's real wire+verification code — without fighting that singleton.
// Test 1 has no such conflict (there is genuinely nothing to find), so it
// uses the real handler unmodified.

import (
	"bufio"
	"encoding/json"
	"testing"
	"time"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

func withTimeoutCertRejoinEnabled(t *testing.T) {
	t.Helper()
	prev := TimeoutCertRejoinEnabled
	TimeoutCertRejoinEnabled = true
	t.Cleanup(func() { TimeoutCertRejoinEnabled = prev })
}

// TestHandleTimeoutCertRejoinStream_NotFound covers the expected steady
// state: most heights never time out, so most rejoin requests get a
// legitimate "not found" — not an error.
func TestHandleTimeoutCertRejoinStream_NotFound(t *testing.T) {
	withTimeoutCertRejoinEnabled(t)

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

	server.SetStreamHandler(config.TimeoutCertRejoinProtocol, HandleTimeoutCertRejoinStream)
	client.Peerstore().AddAddrs(server.ID(), server.Addrs(), time.Hour)
	if err := client.Connect(t.Context(), peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	const height = uint64(900201) // fixed, disjoint from other test files' heights
	newPeriod, accepted, err := RequestLatestTimeoutCertificateFromPeers(client, []peer.ID{server.ID()}, height)
	if err != nil {
		t.Fatalf("expected no error for a legitimate not-found answer, got: %v", err)
	}
	if accepted {
		t.Fatalf("expected accepted=false when the peer has no certificate, got newPeriod=%d", newPeriod)
	}
}

// serveTimeoutCert installs a minimal stream handler on h that always
// answers with resp, regardless of the request payload. See the file-level
// NOTE above for why this is used instead of the real handler in tests 2-4.
func serveTimeoutCert(h host.Host, resp TimeoutCertRejoinResponse) {
	h.SetStreamHandler(config.TimeoutCertRejoinProtocol, func(s network.Stream) {
		defer s.Close()
		_, _ = bufio.NewReader(s).ReadString('\n') // drain the request
		payload, _ := json.Marshal(resp)
		payload = append(payload, '\n')
		_, _ = s.Write(payload)
	})
}

// TestRequestLatestTimeoutCertificateFromPeers_FoundAndVerified is the
// decisive end-to-end proof: a peer serves a genuine, quorum-certified
// TimeoutCertificate over the real RPC, and the CLIENT independently
// verifies it (via the same AcceptIncomingTimeoutCertificate path gossip
// uses) and advances its own PeriodStore.
func TestRequestLatestTimeoutCertificateFromPeers_FoundAndVerified(t *testing.T) {
	withTimeoutCertRejoinEnabled(t)

	const height = uint64(900202)
	kps := newKeypairs(t, 4) // quorum = ceil(2*4/3) = 3
	setTestEligibility(t, map[string]string{
		kps[0].id: hexPub(kps[0]), kps[1].id: hexPub(kps[1]),
		kps[2].id: hexPub(kps[2]), kps[3].id: hexPub(kps[3]),
	})
	cert := buildCertificate(t, kps, height, 1)

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

	serveTimeoutCert(server, TimeoutCertRejoinResponse{Found: true, Cert: *cert})
	client.Peerstore().AddAddrs(server.ID(), server.Addrs(), time.Hour)
	if err := client.Connect(t.Context(), peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// The CLIENT's own PeriodStore starts fresh — as if it just restarted or
	// rejoined and never saw the votes that produced this certificate.
	savedStore := DefaultPeriodStore
	DefaultPeriodStore = NewPeriodStore()
	t.Cleanup(func() { DefaultPeriodStore = savedStore })
	if got := DefaultPeriodStore.PeriodFor(height); got != 0 {
		t.Fatalf("sanity: client should start at period 0, got %d", got)
	}

	newPeriod, accepted, err := RequestLatestTimeoutCertificateFromPeers(client, []peer.ID{server.ID()}, height)
	if err != nil {
		t.Fatalf("RequestLatestTimeoutCertificateFromPeers: unexpected error: %v", err)
	}
	if !accepted {
		t.Fatal("expected the client to accept the peer's genuine certificate")
	}
	if newPeriod != 1 {
		t.Fatalf("newPeriod = %d, want 1", newPeriod)
	}
	if got := DefaultPeriodStore.PeriodFor(height); got != 1 {
		t.Fatalf("client's PeriodStore.PeriodFor(height) = %d, want 1 — the RPC must actually advance local state, not just report success", got)
	}
}

// TestRequestLatestTimeoutCertificateFromPeers_RejectsUnverifiableCertificate
// proves the RPC layer adds no new trust: a peer that answers with a
// certificate signed by keys OUTSIDE the client's own eligible pool for that
// height must be rejected, exactly as it would be if the same bytes had
// arrived over gossip.
func TestRequestLatestTimeoutCertificateFromPeers_RejectsUnverifiableCertificate(t *testing.T) {
	withTimeoutCertRejoinEnabled(t)

	const height = uint64(900203)
	// The signer used to BUILD the certificate...
	signerKps := newKeypairs(t, 1)
	// ...is deliberately NOT in the client's eligibility source for this
	// height, simulating a lying/forging peer.
	otherKps := newKeypairs(t, 1)
	setTestEligibility(t, map[string]string{"someone-else": hexPub(otherKps[0])})

	cert, ok, err := TallyTimeoutVotes(
		[]TimeoutVote{mustSignTimeoutVote(t, signerKps[0], height, 1)},
		height, 1, 1, pubKeyMap(signerKps), nil,
	)
	if err != nil || !ok {
		t.Fatalf("building the forged-context certificate: ok=%v err=%v", ok, err)
	}

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

	serveTimeoutCert(server, TimeoutCertRejoinResponse{Found: true, Cert: *cert})
	client.Peerstore().AddAddrs(server.ID(), server.Addrs(), time.Hour)
	if err := client.Connect(t.Context(), peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	savedStore := DefaultPeriodStore
	DefaultPeriodStore = NewPeriodStore()
	t.Cleanup(func() { DefaultPeriodStore = savedStore })

	newPeriod, accepted, _ := RequestLatestTimeoutCertificateFromPeers(client, []peer.ID{server.ID()}, height)
	if accepted {
		t.Fatalf("expected the client to REJECT a certificate signed by keys outside its eligible pool, got accepted with newPeriod=%d", newPeriod)
	}
	if got := DefaultPeriodStore.PeriodFor(height); got != 0 {
		t.Fatalf("client's PeriodStore must be untouched by a rejected certificate, got period=%d", got)
	}
}

// TestRequestLatestTimeoutCertificateFromPeers_TriesNextPeerOnFailure proves
// one bad/unreachable peer cannot stall a rejoining node when a second,
// good peer is available.
func TestRequestLatestTimeoutCertificateFromPeers_TriesNextPeerOnFailure(t *testing.T) {
	withTimeoutCertRejoinEnabled(t)

	const height = uint64(900204)
	kps := newKeypairs(t, 1)
	setTestEligibility(t, map[string]string{kps[0].id: hexPub(kps[0])})
	cert := buildCertificate(t, kps, height, 1)

	badServer, err := libp2p.New() // no stream handler registered — every request to it fails
	if err != nil {
		t.Fatalf("libp2p.New badServer: %v", err)
	}
	defer badServer.Close()
	goodServer, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New goodServer: %v", err)
	}
	defer goodServer.Close()
	serveTimeoutCert(goodServer, TimeoutCertRejoinResponse{Found: true, Cert: *cert})

	client, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New client: %v", err)
	}
	defer client.Close()
	client.Peerstore().AddAddrs(goodServer.ID(), goodServer.Addrs(), time.Hour)
	if err := client.Connect(t.Context(), peer.AddrInfo{ID: goodServer.ID(), Addrs: goodServer.Addrs()}); err != nil {
		t.Fatalf("connect goodServer: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	// badServer is intentionally NOT connected/reachable — requestLatestTimeoutCertificate's
	// dial will fail, exercising the "try next peer" branch.

	savedStore := DefaultPeriodStore
	DefaultPeriodStore = NewPeriodStore()
	t.Cleanup(func() { DefaultPeriodStore = savedStore })

	newPeriod, accepted, err := RequestLatestTimeoutCertificateFromPeers(
		client, []peer.ID{badServer.ID(), goodServer.ID()}, height,
	)
	if err != nil {
		t.Fatalf("expected the good second peer to succeed despite the first failing, got error: %v", err)
	}
	if !accepted || newPeriod != 1 {
		t.Fatalf("expected accepted=true newPeriod=1 from the good peer, got accepted=%v newPeriod=%d", accepted, newPeriod)
	}
}

// mustSignTimeoutVote is a small local convenience so the "unverifiable
// certificate" test doesn't need to spell out the domain plumbing inline.
func mustSignTimeoutVote(t *testing.T, kp keypair, height, period uint64) TimeoutVote {
	t.Helper()
	v, err := SignTimeoutVote(kp.priv, kp.id, BLS_Signer.DomainChainID(), height, period)
	if err != nil {
		t.Fatalf("sign timeout vote: %v", err)
	}
	return v
}
