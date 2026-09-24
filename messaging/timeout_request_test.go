package messaging

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/internal/roundlock"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Heights 910xxx: disjoint from every other test file's use of the shared
// DefaultPeriodStore / defaultTimeoutVoteCollector singletons.

func libp2pIdentity(t *testing.T) (crypto.PrivKey, string) {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}
	return priv, pid.String()
}

func TestTimeoutRequest_VerifiesOnlyAgainstThePinnedSequencer(t *testing.T) {
	seqPriv, seqID := libp2pIdentity(t)
	otherPriv, otherID := libp2pIdentity(t)
	chain := BLS_Signer.DomainChainID()

	req, err := SignTimeoutRequest(seqPriv, seqID, chain, 910001, 0)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := VerifyTimeoutRequest(req, seqID, chain); err != nil {
		t.Fatalf("request signed by the pinned sequencer must verify: %v", err)
	}

	// Any other peer - even one that claims to be the sequencer - is rejected.
	forged, _ := SignTimeoutRequest(otherPriv, seqID, chain, 910001, 0)
	if err := VerifyTimeoutRequest(forged, seqID, chain); !errors.Is(err, ErrTimeoutRequestSignature) {
		t.Fatalf("request signed by a non-pinned peer must be rejected, got %v", err)
	}
	// Pinning a different sequencer rejects the genuine one.
	if err := VerifyTimeoutRequest(req, otherID, chain); !errors.Is(err, ErrTimeoutRequestSignature) {
		t.Fatalf("request must not verify against a different pin, got %v", err)
	}
}

func TestTimeoutRequest_TamperAndReplayAreRejected(t *testing.T) {
	seqPriv, seqID := libp2pIdentity(t)
	chain := BLS_Signer.DomainChainID()
	req, _ := SignTimeoutRequest(seqPriv, seqID, chain, 910002, 3)

	h := req
	h.Height++
	if err := VerifyTimeoutRequest(h, seqID, chain); err == nil {
		t.Fatalf("a request moved to another height must not verify")
	}
	p := req
	p.Period++
	if err := VerifyTimeoutRequest(p, seqID, chain); err == nil {
		t.Fatalf("a request moved to another period must not verify")
	}
	if err := VerifyTimeoutRequest(req, seqID, chain+1); err == nil {
		t.Fatalf("a request must not verify on another chain")
	}
}

func TestTimeoutRequest_FailsClosedWithoutAUsablePin(t *testing.T) {
	seqPriv, seqID := libp2pIdentity(t)
	req, _ := SignTimeoutRequest(seqPriv, seqID, 1, 910003, 0)
	if err := VerifyTimeoutRequest(req, "", 1); !errors.Is(err, ErrTimeoutRequestNoPin) {
		t.Fatalf("no pin must fail closed with ErrTimeoutRequestNoPin, got %v", err)
	}
	if err := VerifyTimeoutRequest(req, "not-a-peer-id", 1); !errors.Is(err, ErrTimeoutRequestBadPin) {
		t.Fatalf("unparseable pin must fail closed with ErrTimeoutRequestBadPin, got %v", err)
	}
}

func TestDecideTimeoutRequest(t *testing.T) {
	const seq = "sequencer-id"
	req := TimeoutRequest{Height: 910004, Period: 2}

	cases := []struct {
		name  string
		self  string
		local uint64
		pre   roundlock.Side // side already signed for the round, 0 = none
		want  timeoutRequestAction
	}{
		{"sequencer ignores its own request", seq, 2, 0, actIgnoreSelf},
		{"stale: already past this period", "node", 3, 0, actIgnoreStale},
		{"ahead: missing the certificate for this period", "node", 1, 0, actIgnoreAhead},
		{"already signed a block result for the round", "node", 2, roundlock.Block, actRefuseBlockSig},
		{"current round, nothing signed: sign", "node", 2, 0, actSign},
		{"current round, timeout already signed: sign again (idempotent)", "node", 2, roundlock.Timeout, actSign},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := roundlock.NewLedger()
			r := roundlock.Round{Height: req.Height, Period: req.Period}
			if tc.pre != 0 {
				l.TryLock(r, tc.pre)
			}
			if got := decideTimeoutRequest(req, tc.self, seq, tc.local, l); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDecideTimeoutRequest_SigningLocksOutALaterBlockResult(t *testing.T) {
	l := roundlock.NewLedger()
	req := TimeoutRequest{Height: 910005, Period: 0}
	if got := decideTimeoutRequest(req, "node", "seq", 0, l); got != actSign {
		t.Fatalf("want sign, got %s", got)
	}
	if ok, _ := l.TryLock(roundlock.Round{Height: 910005, Period: 0}, roundlock.Block); ok {
		t.Fatalf("after signing a timeout vote, a block result for the same round must be refused")
	}
}

func TestDecideTimeoutRequest_RefusalDoesNotLockTheRound(t *testing.T) {
	// A node that ignores a request (e.g. it is ahead/behind) must stay free
	// to sign a block result for the round.
	l := roundlock.NewLedger()
	req := TimeoutRequest{Height: 910006, Period: 1}
	if got := decideTimeoutRequest(req, "node", "seq", 0, l); got != actIgnoreAhead {
		t.Fatalf("want ignore_ahead, got %s", got)
	}
	if _, signed := l.Signed(roundlock.Round{Height: 910006, Period: 1}); signed {
		t.Fatalf("an ignored request must not lock the round")
	}
}

// TestTimeoutQuorum_PoolOf29 is the production shape: the timeout quorum is
// 2/3 of the whole eligible pool, 29 peers -> 20 signatures. Before fix 3
// only the sequencer ever signed, so it could never form; with every eligible
// node co-signing on the sequencer's request it does.
func TestTimeoutQuorum_PoolOf29(t *testing.T) {
	const height, failed = 910007, 0
	kps := newKeypairs(t, 29)
	pub := pubKeyMap(kps)
	chain := BLS_Signer.DomainChainID()

	sign := func(n int) []TimeoutVote {
		votes := make([]TimeoutVote, 0, n)
		for i := 0; i < n; i++ {
			v, err := SignTimeoutVote(kps[i].priv, kps[i].id, chain, height, failed+1)
			if err != nil {
				t.Fatalf("sign %d: %v", i, err)
			}
			votes = append(votes, v)
		}
		return votes
	}

	// Before the fix: one signer.
	if _, ok, err := TallyTimeoutVotes(sign(1), height, failed+1, len(kps), pub, nil); err != nil || ok {
		t.Fatalf("a single signer must never reach the pool quorum (ok=%v err=%v)", ok, err)
	}
	if _, ok, _ := TallyTimeoutVotes(sign(19), height, failed+1, len(kps), pub, nil); ok {
		t.Fatalf("19 of 29 is below 2/3 and must not certify")
	}
	cert, ok, err := TallyTimeoutVotes(sign(20), height, failed+1, len(kps), pub, nil)
	if err != nil || !ok {
		t.Fatalf("20 of 29 must certify (ok=%v err=%v)", ok, err)
	}
	if okv, err := VerifyTimeoutCertificate(*cert, len(kps), pub); err != nil || !okv {
		t.Fatalf("the certificate must verify against the pool (ok=%v err=%v)", okv, err)
	}

	// Mutual exclusion at tally time: the buddies that signed a block result
	// this round are excluded, so they cannot help reach 2/3.
	excluded := map[string]bool{}
	for i := 0; i < 5; i++ { // the 5 block-result signers
		excluded[kps[i].id] = true
	}
	if _, ok, _ := TallyTimeoutVotes(sign(24), height, failed+1, len(kps), pub, excluded); ok {
		t.Fatalf("24 signers minus 5 excluded = 19 < 20: must not certify")
	}
	if _, ok, _ := TallyTimeoutVotes(sign(25), height, failed+1, len(kps), pub, excluded); !ok {
		t.Fatalf("25 signers minus 5 excluded = 20: must certify")
	}
}

// driveHandler runs the REAL receive handler on a real libp2p host with the
// wiring on, a pinned sequencer, and a local BLS key, and returns the timeout
// votes this node collected for (height, period).
func driveHandler(t *testing.T, msgData []byte, pinned string, wiring bool, height, period uint64) []TimeoutVote {
	t.Helper()
	h, err := libp2p.New()
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	prevWiring, prevPin, prevKey := TimeoutCertWiringEnabled, timeoutRequestPin, timeoutRequestBLSKey
	t.Cleanup(func() {
		TimeoutCertWiringEnabled, timeoutRequestPin, timeoutRequestBLSKey = prevWiring, prevPin, prevKey
	})
	TimeoutCertWiringEnabled = wiring
	timeoutRequestPin = func() string { return pinned }
	blsPriv := newKeypairs(t, 1)[0].priv
	timeoutRequestBLSKey = func() ([]byte, error) { return blsPriv, nil }

	handleTimeoutRequestBroadcast(h, BroadcastMessageStruct{Type: timeoutRequestBroadcastType, Data: string(msgData)})

	defaultTimeoutVoteCollector.mu.Lock()
	defer defaultTimeoutVoteCollector.mu.Unlock()
	var mine []TimeoutVote
	for _, v := range defaultTimeoutVoteCollector.votes[timeoutRoundKey{height, period}] {
		if v.VoterID == h.ID().String() {
			mine = append(mine, v)
		}
	}
	return mine
}

func signedRequestJSON(t *testing.T, priv crypto.PrivKey, id string, height, period uint64) []byte {
	t.Helper()
	req, err := SignTimeoutRequest(priv, id, BLS_Signer.DomainChainID(), height, period)
	if err != nil {
		t.Fatalf("sign request: %v", err)
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestHandleTimeoutRequest_ValidRequestMakesThisNodeSign(t *testing.T) {
	const height = 910101
	seqPriv, seqID := libp2pIdentity(t)
	votes := driveHandler(t, signedRequestJSON(t, seqPriv, seqID, height, 0), seqID, true, height, 1)
	if len(votes) != 1 {
		t.Fatalf("a verified request for this node's round must produce exactly one own timeout vote for period 1, got %d", len(votes))
	}
	if side, ok := roundlock.Default.Signed(roundlock.Round{Height: height, Period: 0}); !ok || side != roundlock.Timeout {
		t.Fatalf("signing must lock round (h, 0) as timeout, got %v %v", side, ok)
	}
}

func TestHandleTimeoutRequest_ForgedRequestIsIgnored(t *testing.T) {
	const height = 910102
	_, seqID := libp2pIdentity(t)
	attackerPriv, attackerID := libp2pIdentity(t)
	votes := driveHandler(t, signedRequestJSON(t, attackerPriv, attackerID, height, 0), seqID, true, height, 1)
	if len(votes) != 0 {
		t.Fatalf("a request not signed by the pinned sequencer must not produce a vote")
	}
	if _, locked := roundlock.Default.Signed(roundlock.Round{Height: height, Period: 0}); locked {
		t.Fatalf("a rejected request must not lock the round")
	}
}

func TestHandleTimeoutRequest_WiringOffIsNoOp(t *testing.T) {
	const height = 910103
	seqPriv, seqID := libp2pIdentity(t)
	if votes := driveHandler(t, signedRequestJSON(t, seqPriv, seqID, height, 0), seqID, false, height, 1); len(votes) != 0 {
		t.Fatalf("with JMDN_TIMEOUT_CERT_WIRING off the handler must do nothing")
	}
}

func TestHandleTimeoutRequest_RefusesAfterBlockResult(t *testing.T) {
	const height = 910104
	roundlock.Default.TryLock(roundlock.Round{Height: height, Period: 0}, roundlock.Block)
	seqPriv, seqID := libp2pIdentity(t)
	if votes := driveHandler(t, signedRequestJSON(t, seqPriv, seqID, height, 0), seqID, true, height, 1); len(votes) != 0 {
		t.Fatalf("a node that signed a block result for the round must not sign a timeout vote for it")
	}
}

// TestTimeoutRequest_SequencerBroadcastReachesPeerThatSigns runs the fix-3
// trigger over a REAL libp2p connection: a request signed with the sequencer
// host's identity key crosses the wire, the peer's real, unmodified
// HandleBroadcastStream dispatches it, and the peer signs its own timeout
// vote. This covers the host-key signing, the wire format and the dispatch in
// broadcast.go, not just the handler.
func TestTimeoutRequest_SequencerBroadcastReachesPeerThatSigns(t *testing.T) {
	const height = uint64(910201)
	seq, err := libp2p.New()
	if err != nil {
		t.Fatalf("seq host: %v", err)
	}
	defer seq.Close()
	node, err := libp2p.New()
	if err != nil {
		t.Fatalf("node host: %v", err)
	}
	defer node.Close()

	node.SetStreamHandler(config.BroadcastProtocol, HandleBroadcastStream)
	prevHost := getHostInstance()
	SetHostInstance(node)
	t.Cleanup(func() { SetHostInstance(prevHost) })

	prevWiring, prevPin, prevKey := TimeoutCertWiringEnabled, timeoutRequestPin, timeoutRequestBLSKey
	t.Cleanup(func() {
		TimeoutCertWiringEnabled, timeoutRequestPin, timeoutRequestBLSKey = prevWiring, prevPin, prevKey
	})
	TimeoutCertWiringEnabled = true
	timeoutRequestPin = func() string { return seq.ID().String() }
	blsPriv := newKeypairs(t, 1)[0].priv
	timeoutRequestBLSKey = func() ([]byte, error) { return blsPriv, nil }

	seq.Peerstore().AddAddrs(node.ID(), node.Addrs(), time.Hour)
	if err := seq.Connect(t.Context(), peer.AddrInfo{ID: node.ID(), Addrs: node.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Signed with the sequencer HOST's own identity key, exactly as
	// broadcastTimeoutRequest does (h.Peerstore().PrivKey(h.ID())). Sent with
	// sendRawEnvelope rather than sendTimeoutGossip because the seen-message
	// cache is one map per process: the sender marking its own message seen
	// would make the in-process receiver drop it - a test artifact documented
	// on TestEndToEndGossipOverRealNetwork, not a production behaviour.
	req, err := SignTimeoutRequest(seq.Peerstore().PrivKey(seq.ID()), seq.ID().String(), BLS_Signer.DomainChainID(), height, 0)
	if err != nil {
		t.Fatalf("sign with host key: %v", err)
	}
	sendRawEnvelope(t, seq, node.ID(), BroadcastMessageStruct{
		ID:        "timeout-request-test-910201",
		Sender:    seq.ID().String(),
		Content:   timeoutRequestBroadcastType + " broadcast",
		Timestamp: time.Now().Unix(),
		Type:      timeoutRequestBroadcastType,
		Data:      string(mustJSON(t, req)),
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		defaultTimeoutVoteCollector.mu.Lock()
		votes := defaultTimeoutVoteCollector.votes[timeoutRoundKey{height, 1}]
		defaultTimeoutVoteCollector.mu.Unlock()
		for _, v := range votes {
			if v.VoterID == node.ID().String() {
				return // the peer received the sequencer's request and signed
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the peer never signed a timeout vote after the sequencer's request")
}

// TestEnsurePeriodForBlock_FetchesMissingCertificate: a node that missed the
// certificate gossip receives a block claiming Period 1. Before verifying, it
// must fetch and independently verify the certificate from a peer, so its
// PeriodStore reaches 1 instead of failing closed with period_not_synced.
// Turning on the wiring alone must be enough (it implies the rejoin RPC).
func TestEnsurePeriodForBlock_FetchesMissingCertificate(t *testing.T) {
	const height = uint64(910202)
	kps := newKeypairs(t, 4)
	setTestEligibility(t, map[string]string{
		kps[0].id: hexPub(kps[0]), kps[1].id: hexPub(kps[1]),
		kps[2].id: hexPub(kps[2]), kps[3].id: hexPub(kps[3]),
	})
	cert := buildCertificate(t, kps, height, 1)

	server, err := libp2p.New()
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer server.Close()
	client, err := libp2p.New()
	if err != nil {
		t.Fatalf("client: %v", err)
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

	prevWiring, prevRejoin := TimeoutCertWiringEnabled, TimeoutCertRejoinEnabled
	t.Cleanup(func() { TimeoutCertWiringEnabled, TimeoutCertRejoinEnabled = prevWiring, prevRejoin })
	TimeoutCertRejoinEnabled = false

	block := &config.ZKBlock{BlockNumber: height, Period: 1}

	TimeoutCertWiringEnabled = false
	ensurePeriodForBlock(client, block)
	if got := DefaultPeriodStore.PeriodFor(height); got != 0 {
		t.Fatalf("wiring off: catch-up must not run, period = %d", got)
	}

	TimeoutCertWiringEnabled = true
	ensurePeriodForBlock(client, block)
	if got := DefaultPeriodStore.PeriodFor(height); got != 1 {
		t.Fatalf("wiring on: the missing certificate must be fetched and adopted, period = %d, want 1", got)
	}
}

func TestMaybeStartTimeoutFlow_RespectsTheRoundLock(t *testing.T) {
	const height = uint64(910301)
	h, err := libp2p.New()
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	defer h.Close()
	prevWiring, prevKey := TimeoutCertWiringEnabled, timeoutRequestBLSKey
	t.Cleanup(func() { TimeoutCertWiringEnabled, timeoutRequestBLSKey = prevWiring, prevKey })
	TimeoutCertWiringEnabled = true
	blsPriv := newKeypairs(t, 1)[0].priv
	timeoutRequestBLSKey = func() ([]byte, error) { return blsPriv, nil }

	ownVote := func(height uint64) bool {
		defaultTimeoutVoteCollector.mu.Lock()
		defer defaultTimeoutVoteCollector.mu.Unlock()
		for _, v := range defaultTimeoutVoteCollector.votes[timeoutRoundKey{height, 1}] {
			if v.VoterID == h.ID().String() {
				return true
			}
		}
		return false
	}

	// Positive control: an unlocked round IS signed, so the refusal below is
	// the lock at work and not a missing key.
	MaybeStartTimeoutFlow(h, height+1, nil)
	if !ownVote(height + 1) {
		t.Fatalf("control: with the round unlocked the sequencer must sign its own timeout vote")
	}

	// This node already signed the block side of round (height, 0).
	roundlock.Default.TryLock(roundlock.Round{Height: height, Period: 0}, roundlock.Block)
	MaybeStartTimeoutFlow(h, height, nil)
	if ownVote(height) {
		t.Fatalf("the sequencer must not sign a timeout vote for a round it signed the block side of")
	}
}
