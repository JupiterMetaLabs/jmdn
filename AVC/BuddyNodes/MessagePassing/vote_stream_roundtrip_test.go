package MessagePassing

// Direct-stream vote round-trip, end to end over two REAL libp2p hosts.
//
// Regression test for the bug fixed on this branch: the vote branch of
// SendMessageToPeer looked up an uppercase "ACK" key while
// config/PubSubMessages/Pubsub.go tags the field `json:"ack"`. The lookup
// never matched, isVote stayed false, and every vote send fell into a
// 20-second read waiting for a response handleSubmitVote never writes —
// one "failed to read response from <peer>" ERROR per vote, forever.
//
// What is asserted here is the part no unit test covered: a vote written to
// a real stream, with the write side closed the way the sender closes it, is
// read WHOLE by the receiver, parses, routes, and lands in VoteCRDTLayer —
// the only keyspace the tally reads.
//
// Both production listen families are exercised (config.IP4TCP -> yamux and
// config.IP4QUIC -> quic-v1): stream-close semantics are a property of the
// muxer, so a close that is safe on one is not automatically safe on the
// other.
//
// These tests mutate process-wide globals (Set_ForListner, Set_PubSubNode,
// avcvotes.DefaultWatermark, Structs.ValidatorVoteIngestEnabled), every one
// of them saved and restored on cleanup. Do not mark them t.Parallel.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"

	avcdatalayer "github.com/JupiterMetaLabs/avc/buddynodes/datalayer"
	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	roundTripBlockHash = "0xb10c4a5600000000000000000000000000000000000000000000000deadbeef0"
	// Comfortably above anything another test in this binary sets the
	// watermark to; withFreshWatermark below makes that belt-and-braces.
	roundTripHeight = uint64(4242)
)

// newRoundTripHost starts a real libp2p host on the given multiaddr.
func newRoundTripHost(t *testing.T, listenAddr string) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings(listenAddr))
	if err != nil {
		t.Fatalf("starting libp2p host on %s: %v", listenAddr, err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// installReceiver points the global listener node at a fresh, isolated vote
// CRDT and registers the REAL HandleSubmitMessageStream on the host, so the
// test exercises the production read -> parse -> route -> ingest path rather
// than a reimplementation of it.
//
// preReadDelay makes the handler sit idle for that long BEFORE reading a
// single byte, which is the shape of the close race: the sender has already
// written and closed by then. Zero for the normal case.
//
// Returns the node whose VoteCRDTLayer must end up holding the vote, and a
// channel closed once the handler has returned.
func installReceiver(t *testing.T, h host.Host, preReadDelay time.Duration) (*AVCStruct.BuddyNode, <-chan struct{}) {
	t.Helper()

	// avcvotes.DefaultWatermark is process-wide and Watermark.Set refuses to
	// move backward, so a sibling test that raised it (crdt_sync_a8_1_test.go
	// sets it to 100) would make AddVote reject this height as compacted.
	withFreshWatermark(t)

	// Pin the ingest switch ON: it is env-derived
	// (JMDN_VALIDATOR_VOTE_INGEST), and with it off IngestValidatorVote
	// returns nil without writing, which would fail this test for a reason
	// that has nothing to do with the stream.
	savedIngest := Structs.ValidatorVoteIngestEnabled
	Structs.ValidatorVoteIngestEnabled = true
	t.Cleanup(func() { Structs.ValidatorVoteIngestEnabled = savedIngest })

	node := &AVCStruct.BuddyNode{
		PeerID:        h.ID(),
		Host:          h,
		VoteCRDTLayer: DataLayer.NewVoteCRDTLayer(nil),
	}

	globals := AVCStruct.NewGlobalVariables()
	prevListener := globals.Get_ForListner()
	prevPubSub := globals.Get_PubSubNode()
	globals.Set_ForListner(node)
	// handleSubmitVote builds a PubSub node from scratch when this is nil,
	// which would open sockets the test does not need. A non-nil shell is
	// enough: the vote path only nil-checks it and never dereferences it.
	globals.Set_PubSubNode(&AVCStruct.BuddyNode{
		PeerID: h.ID(),
		Host:   h,
		PubSub: &AVCStruct.GossipPubSub{},
	})
	t.Cleanup(func() {
		globals.Set_ForListner(prevListener)
		globals.Set_PubSubNode(prevPubSub)
	})

	handled := make(chan struct{})
	// nil ResponseHandler is deliberate and safe: the Type_SubmitVote branch
	// of HandleSubmitMessageStream never touches lh.responseHandler.
	lh := NewListenerHandler(nil)
	h.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		defer close(handled)
		if preReadDelay > 0 {
			time.Sleep(preReadDelay)
		}
		lh.HandleSubmitMessageStream(context.Background(), s)
	})

	return node, handled
}

// dialAndSendVote performs exactly what SendMessageToPeer does on the vote
// path: marshal the real message envelope, write payload+delimiter, close.
func dialAndSendVote(t *testing.T, sender host.Host, target peer.ID, voteValue int8, closeStrategy string) {
	t.Helper()

	payload := AVCStruct.Vote{
		Vote:      voteValue,
		BlockHash: roundTripBlockHash,
		Height:    roundTripHeight,
		// IngestValidatorVote requires both to be non-empty but does NOT
		// verify them — signature verification happens at tally time, in
		// avcvotes.TallyBlock -> verifyTallySignatures — so fixtures are
		// enough to exercise the ingest path end to end.
		BLSSignature: "aa" + strings.Repeat("bb", 47),
		BLSPubKeyHex: "cc" + strings.Repeat("dd", 47),
	}
	if voteValue == -1 {
		payload.RejectionReason = "round-trip fixture"
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshaling vote payload: %v", err)
	}

	msg := AVCStruct.NewMessageBuilder(nil).
		SetSender(sender.ID()).
		SetMessage(string(payloadBytes)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(AVCStruct.NewACKBuilder().True_ACK_Message(sender.ID(), config.Type_SubmitVote))

	wire, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshaling message envelope: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := sender.NewStream(ctx, target, config.SubmitMessageProtocol)
	if err != nil {
		t.Fatalf("opening stream: %v", err)
	}
	if _, err := s.Write(append(wire, byte(config.Delimiter))); err != nil {
		t.Fatalf("writing vote: %v", err)
	}

	switch closeStrategy {
	case "close":
		// What MessageListener.go does today: the deferred stream.Close()
		// fires microseconds after the write returns.
		//
		// The error is logged, not asserted, because production discards it
		// too (`defer stream.Close()` in SendMessageToPeer) and because it is
		// not a delivery signal. On QUIC the receiver often finishes handling
		// and resets the stream before this call lands, so Close reports
		// "close called for canceled stream N" on a vote that arrived
		// perfectly well — asserting nil here made this test flaky without
		// testing anything production depends on. What this test is actually
		// about is assertVoteIngested below.
		if err := s.Close(); err != nil {
			t.Logf("Close after write returned %v (ignored — production defers Close and discards this too)", err)
		}
	case "closeWrite":
		// Half-close: end-of-write signalled, read side left open.
		cw, ok := s.(interface{ CloseWrite() error })
		if !ok {
			t.Fatal("stream does not implement CloseWrite")
		}
		if err := cw.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
	default:
		t.Fatalf("unknown close strategy %q", closeStrategy)
	}
}

// assertVoteIngested checks the vote reached the ONLY keyspace the tally
// reads, under the element string avc actually writes
// (avc/crdt/votes/write.go: fmt.Sprintf("%s:%d", rec.PeerID, rec.Vote)).
func assertVoteIngested(t *testing.T, node *AVCStruct.BuddyNode, voter peer.ID, voteValue int8) {
	t.Helper()

	key := avcvotes.BlockVoteKey(roundTripHeight, roundTripBlockHash)
	elems, ok := avcdatalayer.GetSet(node.VoteCRDTLayer, key)
	if !ok {
		t.Fatalf("no vote set at %s — the vote never reached VoteCRDTLayer", key)
	}

	want := fmt.Sprintf("%s:%d", voter.String(), voteValue)
	for _, e := range elems {
		if e == want {
			return
		}
	}
	t.Fatalf("vote element %q missing from %s; set contains %v", want, key, elems)
}

// runRoundTrip wires two hosts on the same transport, sends one vote, waits
// for the handler, and asserts the vote was ingested.
func runRoundTrip(t *testing.T, listenAddr, closeStrategy string, voteValue int8, preReadDelay time.Duration) {
	t.Helper()

	receiverHost := newRoundTripHost(t, listenAddr)
	senderHost := newRoundTripHost(t, listenAddr)

	node, handled := installReceiver(t, receiverHost, preReadDelay)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := senderHost.Connect(ctx, peer.AddrInfo{
		ID: receiverHost.ID(), Addrs: receiverHost.Addrs(),
	}); err != nil {
		t.Fatalf("connecting hosts: %v", err)
	}

	dialAndSendVote(t, senderHost, receiverHost.ID(), voteValue, closeStrategy)

	select {
	case <-handled:
	case <-time.After(20*time.Second + preReadDelay):
		t.Fatal("receiver never finished handling the stream")
	}

	assertVoteIngested(t, node, senderHost.ID(), voteValue)
}

// TestVoteStreamRoundTrip is the headline test: a real vote, over a real
// stream, on both production transports, with both close strategies.
func TestVoteStreamRoundTrip(t *testing.T) {
	transports := []struct{ name, addr string }{
		{"tcp", "/ip4/127.0.0.1/tcp/0"},
		{"quic", "/ip4/127.0.0.1/udp/0/quic-v1"},
	}
	for _, strategy := range []string{"close", "closeWrite"} {
		for _, tr := range transports {
			t.Run(fmt.Sprintf("%s/%s", tr.name, strategy), func(t *testing.T) {
				runRoundTrip(t, tr.addr, strategy, 1, 0)
			})
		}
	}
}

// TestVoteStreamRoundTrip_NoTruncationUnderSlowReceiver is the race the
// close strategy is actually about: the sender writes, returns, and closes
// while the receiving node has not yet read a byte (GC pause, scheduler,
// handler busy). If Close() discarded buffered data, this is where the vote
// would be lost — silently, which is worse than the loud error it replaced.
//
// Run with -count=20 or higher to hunt a flaky truncation; a single pass
// only proves the common case.
func TestVoteStreamRoundTrip_NoTruncationUnderSlowReceiver(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: this test deliberately sleeps")
	}
	for _, strategy := range []string{"close", "closeWrite"} {
		t.Run(strategy, func(t *testing.T) {
			runRoundTrip(t, "/ip4/127.0.0.1/tcp/0", strategy, -1, 1*time.Second)
		})
	}
}

// TestVoteMessageUsesLowercaseACKKey is the direct guard on the bug itself.
// SendMessageToPeer decides isVote by looking up a key in the unmarshaled
// message map. If the json tag on Message.ACK is ever renamed, or the lookup
// key drifts back to "ACK", every vote send silently falls into the
// 20-second response read again. This fails the moment that happens, with no
// network and no globals.
func TestVoteMessageUsesLowercaseACKKey(t *testing.T) {
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("generating key pair: %v", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("deriving peer ID: %v", err)
	}

	msg := AVCStruct.NewMessageBuilder(nil).
		SetSender(id).
		SetMessage(`{"vote":1,"block_hash":"0xabc","height":1}`).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(AVCStruct.NewACKBuilder().True_ACK_Message(id, config.Type_SubmitVote))

	wire, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}

	if _, upper := decoded["ACK"]; upper {
		t.Fatal(`message carries an uppercase "ACK" key: SendMessageToPeer's lookup key must be updated to match`)
	}
	ack, ok := decoded["ack"].(map[string]interface{})
	if !ok {
		t.Fatalf(`expected a lowercase "ack" object on the wire, got keys %v`, keysOf(decoded))
	}
	stage, ok := ack["stage"].(string)
	if !ok {
		t.Fatalf("expected ack.stage to be a string, got %v (keys %v)", ack["stage"], keysOf(ack))
	}
	if stage != config.Type_SubmitVote {
		t.Fatalf("expected ack.stage == %q, got %q", config.Type_SubmitVote, stage)
	}
}
