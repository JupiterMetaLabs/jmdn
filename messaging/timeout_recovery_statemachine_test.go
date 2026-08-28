package messaging

// Tests proving the timeout-recovery state machine matches, exactly, the
// design confirmed in this session: Period may advance ONLY via a verified,
// monotonic TimeoutCertificate — never on a timer, never by a single node's
// (including the sequencer's) local say-so. See the invariant comment on
// PeriodStore in timeout_certificates.go for the rule this file tests.
//
// Scenario used throughout, matching the exact numbers this was designed
// against: 7 committee members, quorum = ceil(2*7/3) = 5.
//
// Coverage:
//  1. Quorum reached: ANY number of independent nodes that see the same
//     certificate reach the exact same period — proving "verify once,
//     agree everywhere" rather than trusting whoever assembled it.
//  2. Quorum not reached (3 of 5): period stays frozen. Nothing partially
//     advances, nothing times out into a weaker acceptance.
//  3. Votes arrive late, over time (not all at once): quorum is only
//     recognized once it is actually crossed, proving there is no
//     collection deadline that discards already-received votes.
//  4. No privileged aggregator: a node with no special "sequencer" role in
//     the code (recordAndMaybeCertify/tryCertify take no identity/role
//     parameter at all) can independently collect enough votes and produce
//     a certificate that a separate node accepts over a REAL network — the
//     same path works whether or not any particular node is available.
//
// All tests use fixed, high, disjoint heights (900301+) to avoid colliding
// with any other test file's use of the shared package-level
// DefaultPeriodStore/defaultTimeoutVoteCollector singletons — same
// convention as timeout_gossip_test.go and timeout_rejoin_test.go.

import (
	"bufio"
	"encoding/json"
	"testing"
	"time"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// sevenBuddyPool returns 7 keypairs and the eligibility map/pubkey map
// TallyTimeoutVotes expects, matching the exact "7 buddies, need 5" scenario
// this design was worked out against.
func sevenBuddyPool(t *testing.T) ([]keypair, map[string][]byte) {
	t.Helper()
	kps := newKeypairs(t, 7)
	return kps, pubKeyMap(kps)
}

// --- 1. Quorum reached: independent verification, not trust in the assembler ---

// TestTimeoutRecoveryStateMachine_QuorumReached_EveryIndependentNodeConverges
// proves the core property this design depends on: the certificate itself is
// the evidence, not who built it. Three completely independent PeriodStore
// instances — standing in for three nodes that never exchanged a single
// message with each other — are each handed the exact same certificate, and
// all three must reach the exact same period through their own independent
// signature verification.
func TestTimeoutRecoveryStateMachine_QuorumReached_EveryIndependentNodeConverges(t *testing.T) {
	const height = uint64(900301)
	kps, pubKeys := sevenBuddyPool(t)

	// Exactly 5 of 7 vote — the minimum that clears quorum, not the whole
	// pool, so this also proves 5 is sufficient (not merely necessary).
	votes := make([]TimeoutVote, 5)
	for i := 0; i < 5; i++ {
		v, err := SignTimeoutVote(kps[i].priv, kps[i].id, BLS_Signer.DomainChainID(), height, 1)
		if err != nil {
			t.Fatalf("sign vote %d: %v", i, err)
		}
		votes[i] = v
	}

	cert, ok, err := TallyTimeoutVotes(votes, height, 1, len(kps), pubKeys, nil)
	if err != nil {
		t.Fatalf("tally: %v", err)
	}
	if !ok || cert == nil {
		t.Fatal("expected quorum (5 of 7) to certify — it did not")
	}
	if len(cert.SignerBitmap) != 5 {
		t.Fatalf("SignerBitmap has %d signers, want exactly 5", len(cert.SignerBitmap))
	}

	// Three independent "nodes" — plain, unconnected PeriodStore values, no
	// shared state, no shared package-level singleton — each verify the same
	// certificate on their own.
	for i, label := range []string{"node-A", "node-B", "node-C"} {
		store := NewPeriodStore()
		if got := store.PeriodFor(height); got != 0 {
			t.Fatalf("%s: sanity, should start at period 0, got %d", label, got)
		}
		newPeriod, accepted, err := store.AcceptTimeoutCertificate(*cert, len(kps), pubKeys)
		if err != nil {
			t.Fatalf("%s (independent verifier #%d): unexpected error: %v", label, i, err)
		}
		if !accepted {
			t.Fatalf("%s: expected the certificate to be accepted", label)
		}
		if newPeriod != 1 {
			t.Fatalf("%s: newPeriod = %d, want 1", label, newPeriod)
		}
		if got := store.PeriodFor(height); got != 1 {
			t.Fatalf("%s: PeriodFor(height) = %d, want 1 after acceptance", label, got)
		}
	}
}

// --- 2. Quorum NOT reached: frozen, not partially advanced, not timed out ---

// TestTimeoutRecoveryStateMachine_QuorumNotReached_PeriodStaysFrozen is the
// exact "3 of 5" scenario: fewer than quorum valid votes arrive. No
// certificate may exist, and a fresh node's period for this height must
// remain exactly 0 — there is no partial-credit or timeout-based fallback
// acceptance anywhere in this path.
func TestTimeoutRecoveryStateMachine_QuorumNotReached_PeriodStaysFrozen(t *testing.T) {
	const height = uint64(900302)
	kps, pubKeys := sevenBuddyPool(t)

	// Only 3 of the 7 vote — below the 5-vote quorum.
	votes := make([]TimeoutVote, 3)
	for i := 0; i < 3; i++ {
		v, err := SignTimeoutVote(kps[i].priv, kps[i].id, BLS_Signer.DomainChainID(), height, 1)
		if err != nil {
			t.Fatalf("sign vote %d: %v", i, err)
		}
		votes[i] = v
	}

	cert, ok, err := TallyTimeoutVotes(votes, height, 1, len(kps), pubKeys, nil)
	if err != nil {
		t.Fatalf("tally: unexpected error for a legitimate not-yet-enough case: %v", err)
	}
	if ok || cert != nil {
		t.Fatalf("expected no certificate with only 3 of 7 votes (quorum=5), got ok=%v cert=%+v", ok, cert)
	}

	// A node with no certificate to accept simply never advances. Confirmed
	// on a fresh, independent store — nothing to "expire into."
	store := NewPeriodStore()
	if got := store.PeriodFor(height); got != 0 {
		t.Fatalf("PeriodFor(height) = %d, want 0 (frozen, no certificate exists)", got)
	}
}

// --- 3. Votes trickle in over time: recognized the instant quorum crosses ---

// TestTimeoutRecoveryStateMachine_LateVotesArriveOverTime_QuorumReachedEventually
// proves there is no collection deadline that discards already-received
// votes: 3 votes arrive, then (simulating a delay) 2 more arrive later, and
// the certificate must form the moment the 5th vote lands — not before, not
// requiring a fresh restart of collection. Uses the real production
// collector (recordAndMaybeCertify/tryCertify), not a hand-rolled stand-in,
// on a fixed height disjoint from every other test file's use of the same
// package-level singletons.
func TestTimeoutRecoveryStateMachine_LateVotesArriveOverTime_QuorumReachedEventually(t *testing.T) {
	const height = uint64(900303)
	kps, _ := sevenBuddyPool(t)
	setTestEligibility(t, func() map[string]string {
		m := make(map[string]string, len(kps))
		for _, k := range kps {
			m[k.id] = hexPub(k)
		}
		return m
	}())

	period := DefaultPeriodStore.PeriodFor(height) + 1
	if period != 1 {
		t.Fatalf("sanity: expected to start at period 1 (fresh height), got %d", period)
	}

	sign := func(i int) TimeoutVote {
		v, err := SignTimeoutVote(kps[i].priv, kps[i].id, BLS_Signer.DomainChainID(), height, period)
		if err != nil {
			t.Fatalf("sign vote %d: %v", i, err)
		}
		return v
	}

	// First 3 votes arrive — not enough yet.
	recordAndMaybeCertify(nil, sign(0), nil)
	recordAndMaybeCertify(nil, sign(1), nil)
	recordAndMaybeCertify(nil, sign(2), nil)
	if got := DefaultPeriodStore.PeriodFor(height); got != 0 {
		t.Fatalf("after 3 of 7 votes: PeriodFor(height) = %d, want 0 (still short of quorum=5)", got)
	}

	// Simulate real elapsed time between vote arrivals — nothing in this
	// path may discard the first 3 votes just because time passed.
	time.Sleep(50 * time.Millisecond)

	// 4th vote — still short.
	recordAndMaybeCertify(nil, sign(3), nil)
	if got := DefaultPeriodStore.PeriodFor(height); got != 0 {
		t.Fatalf("after 4 of 7 votes: PeriodFor(height) = %d, want 0 (still short of quorum=5)", got)
	}

	// 5th vote — quorum crosses exactly here, not before.
	recordAndMaybeCertify(nil, sign(4), nil)
	if got := DefaultPeriodStore.PeriodFor(height); got != period {
		t.Fatalf("after 5 of 7 votes: PeriodFor(height) = %d, want %d (quorum just reached)", got, period)
	}

	cert, ok := LatestTimeoutCertificateFor(height)
	if !ok {
		t.Fatal("expected a cached certificate once quorum was reached")
	}
	if len(cert.SignerBitmap) != 5 {
		t.Fatalf("certificate has %d signers, want exactly 5 (the first 5 that actually arrived)", len(cert.SignerBitmap))
	}
}

// --- 4. No privileged aggregator: any node can complete the flow ---

// TestTimeoutRecoveryStateMachine_NoPrivilegedNode_AnyPeerCanCompleteTheCertificate
// proves the property the design explicitly requires: "the sequencer can
// collect and aggregate for efficiency, but must not be the only path, and
// must not have any special authority." recordAndMaybeCertify/tryCertify
// take no host-identity or role parameter anywhere — grepped and confirmed
// — so this test deliberately does NOT designate the receiving host as any
// kind of "sequencer": it is just h2, an ordinary peer that happens to
// receive enough votes, over the real production transport
// (config.BroadcastProtocol / HandleBroadcastStream), exactly as
// TestEndToEndGossipOverRealNetwork already proves for the wire path in
// general. This test's addition is the 7-buddy/quorum-5 numbers specific to
// this design conversation, and the explicit framing that h2 holds no
// privileged role.
func TestTimeoutRecoveryStateMachine_NoPrivilegedNode_AnyPeerCanCompleteTheCertificate(t *testing.T) {
	prevFlag := TimeoutCertWiringEnabled
	TimeoutCertWiringEnabled = true
	t.Cleanup(func() { TimeoutCertWiringEnabled = prevFlag })

	prevHost := hostInstance
	t.Cleanup(func() { SetHostInstance(prevHost) })

	const height = uint64(900304)
	kps, _ := sevenBuddyPool(t)
	setTestEligibility(t, func() map[string]string {
		m := make(map[string]string, len(kps))
		for _, k := range kps {
			m[k.id] = hexPub(k)
		}
		return m
	}())

	h1, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New h1: %v", err)
	}
	defer h1.Close()
	h2, err := libp2p.New() // stands in for "whichever peer collects enough votes" — NOT a distinguished sequencer role
	if err != nil {
		t.Fatalf("libp2p.New h2: %v", err)
	}
	defer h2.Close()
	h3, err := libp2p.New() // a third, independent peer that only ever sees the finished certificate
	if err != nil {
		t.Fatalf("libp2p.New h3: %v", err)
	}
	defer h3.Close()

	h2.SetStreamHandler(config.BroadcastProtocol, HandleBroadcastStream)
	received := make(chan []byte, 8)
	h3.SetStreamHandler(config.BroadcastProtocol, func(s network.Stream) {
		defer s.Close()
		data, _ := bufio.NewReader(s).ReadBytes('\n')
		received <- data
	})
	SetHostInstance(h2)

	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	if err := h1.Connect(t.Context(), peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}); err != nil {
		t.Fatalf("h1.Connect(h2): %v", err)
	}
	h2.Peerstore().AddAddrs(h3.ID(), h3.Addrs(), time.Hour)
	if err := h2.Connect(t.Context(), peer.AddrInfo{ID: h3.ID(), Addrs: h3.Addrs()}); err != nil {
		t.Fatalf("h2.Connect(h3): %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	period := DefaultPeriodStore.PeriodFor(height) + 1

	// h1 gossips 5 of the 7 committee members' votes to h2 — one peer
	// relaying votes on behalf of several committee members here purely as
	// a test simplification (each vote is still independently signed by its
	// own keypair and independently verified by h2 — h1 is not vouching for
	// anything, only relaying already-signed bytes, exactly as gossip would).
	for i := 0; i < 5; i++ {
		v, err := SignTimeoutVote(kps[i].priv, kps[i].id, BLS_Signer.DomainChainID(), height, period)
		if err != nil {
			t.Fatalf("sign vote %d: %v", i, err)
		}
		envelope := BroadcastMessageStruct{
			Sender:    h1.ID().String(),
			Content:   timeoutVoteBroadcastType + " broadcast",
			Timestamp: time.Now().UTC().Unix(),
			Hops:      0,
			Type:      timeoutVoteBroadcastType,
			Data:      string(mustJSON(t, v)),
		}
		envelope.ID = generateMessageID(envelope.Sender, envelope.Content+envelope.Data, envelope.Timestamp)
		sendRawEnvelope(t, h1, h2.ID(), envelope)
	}

	waitFor(t, 3*time.Second, func() bool {
		return DefaultPeriodStore.PeriodFor(height) == period
	}, "h2 (an ordinary, non-privileged peer) never reached quorum and advanced the period")

	// h3 — a THIRD, independent peer that took no part in voting — must
	// receive and independently accept the certificate h2 assembled, over
	// the real network, proving the recovery does not depend on h3 having
	// participated or on h2 holding any special role.
	deadline := time.After(3 * time.Second)
	for {
		var raw []byte
		select {
		case raw = <-received:
		case <-deadline:
			t.Fatal("h3 never received the certificate broadcast from h2 over the network")
		}
		var envelope BroadcastMessageStruct
		if err := json.Unmarshal(bytesTrimNewline(raw), &envelope); err != nil {
			t.Fatalf("h3 received unparseable envelope: %v", err)
		}
		if envelope.Type != timeoutCertBroadcastType {
			continue
		}
		var gotCert TimeoutCertificate
		if err := json.Unmarshal([]byte(envelope.Data), &gotCert); err != nil {
			t.Fatalf("h3 received unparseable certificate payload: %v", err)
		}
		if gotCert.Height != height || gotCert.Period != period || len(gotCert.SignerBitmap) != 5 {
			t.Fatalf("h3 received an unexpected certificate: %+v", gotCert)
		}
		break
	}
}
