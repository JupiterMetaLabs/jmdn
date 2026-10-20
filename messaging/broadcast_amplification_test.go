package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
)

// Regression tests for the testnet block-broadcast amplification
// (docs/BLOCK_BROADCAST_AMPLIFICATION.md): every finalized 2.66 MB block was
// copied ~465 times fleet-wide because vote triggers and finalized blocks fanned
// out to every connected peer and were re-flooded by every receiver.

func newLoopbackHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func connect(t *testing.T, a, b host.Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Connect(ctx, peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

func testBlock(n uint64) *config.ZKBlock {
	return &config.ZKBlock{
		BlockNumber:   n,
		BlockHash:     common.BigToHash(common.Big1),
		ConsensusHash: common.BigToHash(common.Big2),
		StarkProof:    make([]byte, 1024),
		Status:        "proposed",
	}
}

// withVoters injects the voter set voteTriggerTargets resolves for the test.
func withVoters(t *testing.T, voters map[string]string, err error) {
	t.Helper()
	prev := voteTriggerVoters
	voteTriggerVoters = func(uint64) (map[string]string, error) { return voters, err }
	t.Cleanup(func() { voteTriggerVoters = prev })
}

// The trigger reaches every eligible validator (they VOTE) plus the committee
// (it AGGREGATES) — and nobody else: a connected non-validator peer is skipped.
func TestVoteTriggerTargets_VotersPlusCommittee(t *testing.T) {
	seq := newLoopbackHost(t)
	member := newLoopbackHost(t)    // committee
	voter := newLoopbackHost(t)     // eligible validator, not seated
	bystander := newLoopbackHost(t) // connected, not a validator
	connect(t, seq, member)
	connect(t, seq, voter)
	connect(t, seq, bystander)
	withVoters(t, map[string]string{member.ID().String(): "", voter.ID().String(): ""}, nil)

	cm := PubSubMessages.NewConsensusMessageBuilder(nil).
		SetZKBlock(testBlock(1)).
		SetBuddies([]PubSubMessages.Buddy_PeerMultiaddr{{PeerID: member.ID(), Multiaddr: member.Addrs()[0]}})

	targets, scope := voteTriggerTargets(seq, cm)
	if scope != "committee+voters" {
		t.Fatalf("scope = %q, want committee+voters", scope)
	}
	got := map[peer.ID]bool{}
	for _, p := range targets {
		got[p] = true
	}
	if len(targets) != 2 || !got[member.ID()] || !got[voter.ID()] {
		t.Fatalf("targets = %v, want exactly the committee member and the voter", targets)
	}
	if got[bystander.ID()] {
		t.Fatal("a non-validator peer must not receive the vote trigger")
	}
}

// No resolvable voter set → every connected peer plus the committee (fail-open
// delivery; who is COUNTED is enforced at tally).
func TestVoteTriggerTargets_NoVoterSetFallsBackToAllPeers(t *testing.T) {
	seq := newLoopbackHost(t)
	a := newLoopbackHost(t)
	b := newLoopbackHost(t)
	connect(t, seq, a)
	connect(t, seq, b)
	withVoters(t, nil, errors.New("no snapshot"))
	cm := PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(testBlock(1))
	targets, scope := voteTriggerTargets(seq, cm)
	if scope != "all-peers(no-voter-set)" || len(targets) != 2 {
		t.Fatalf("fallback: scope=%q targets=%d, want all-peers(no-voter-set) and 2", scope, len(targets))
	}
}

// A committee member the sequencer is not connected to must be dialled from the
// multiaddr carried in Buddies, not silently skipped.
func TestVoteTriggerTargets_DialsUnconnectedMember(t *testing.T) {
	seq := newLoopbackHost(t)
	member := newLoopbackHost(t)
	if seq.Network().Connectedness(member.ID()) == network.Connected {
		t.Fatal("precondition: hosts must start unconnected")
	}
	cm := PubSubMessages.NewConsensusMessageBuilder(nil).
		SetZKBlock(testBlock(1)).
		SetBuddies([]PubSubMessages.Buddy_PeerMultiaddr{{PeerID: member.ID(), Multiaddr: member.Addrs()[0]}})

	withVoters(t, map[string]string{member.ID().String(): ""}, nil)
	targets, _ := voteTriggerTargets(seq, cm)
	if len(targets) != 1 || targets[0] != member.ID() {
		t.Fatalf("targets = %v, want the dialled member", targets)
	}
	if seq.Network().Connectedness(member.ID()) != network.Connected {
		t.Fatal("expected the sequencer to have dialled the committee member")
	}
}

// A vote trigger must never be re-flooded by a receiver: HandleBroadcastStream
// returns before forwardBroadcast for Type == vote_trigger, and the sender stamps
// Hops = MaxHops so receivers on an older binary skip the re-flood too.
func TestVoteTrigger_NotRebroadcast(t *testing.T) {
	seq := newLoopbackHost(t)
	v1 := newLoopbackHost(t)
	v2 := newLoopbackHost(t)
	connect(t, seq, v1)
	connect(t, v1, v2)

	// v1 handles broadcasts with the real handler; v2 counts what reaches it.
	SetHostInstance(v1)
	t.Cleanup(func() { SetHostInstance(nil) })
	v1.SetStreamHandler(config.BroadcastProtocol, HandleBroadcastStream)
	got := make(chan struct{}, 8)
	v2.SetStreamHandler(config.BroadcastProtocol, func(s network.Stream) {
		defer s.Close()
		got <- struct{}{}
	})

	msg := BroadcastMessageStruct{
		ID: "vt-test", Sender: seq.ID().String(), Content: "x", Timestamp: time.Now().Unix(),
		Hops: 0, // deliberately 0: the Type check alone must stop the re-flood
		Type: voteTriggerBroadcastType, Data: `{"ZKBlock":null}`,
	}
	b, _ := json.Marshal(msg)
	b = append(b, '\n')
	s, err := seq.NewStream(context.Background(), v1.ID(), config.BroadcastProtocol)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	if _, err := s.Write(b); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = s.Close()

	select {
	case <-got:
		t.Fatal("vote trigger was re-flooded from v1 to v2")
	case <-time.After(1500 * time.Millisecond):
	}
}

// The sender-side stamp: BroadcastVoteTrigger emits Hops == MaxHops.
func TestBroadcastVoteTrigger_StampsMaxHops(t *testing.T) {
	seq := newLoopbackHost(t)
	member := newLoopbackHost(t)
	connect(t, seq, member)
	recv := make(chan BroadcastMessageStruct, 1)
	member.SetStreamHandler(config.BroadcastProtocol, func(s network.Stream) {
		defer s.Close()
		var m BroadcastMessageStruct
		if err := json.NewDecoder(s).Decode(&m); err == nil {
			recv <- m
		}
	})
	cm := PubSubMessages.NewConsensusMessageBuilder(nil).
		SetZKBlock(testBlock(7)).
		SetBuddies([]PubSubMessages.Buddy_PeerMultiaddr{{PeerID: member.ID(), Multiaddr: member.Addrs()[0]}})
	withVoters(t, map[string]string{member.ID().String(): ""}, nil)
	if err := BroadcastVoteTrigger(seq, cm); err != nil {
		t.Fatalf("BroadcastVoteTrigger: %v", err)
	}
	select {
	case m := <-recv:
		if m.Hops != config.MaxHops {
			t.Fatalf("Hops = %d, want MaxHops (%d) so old receivers do not re-flood", m.Hops, config.MaxHops)
		}
		if m.Type != voteTriggerBroadcastType {
			t.Fatalf("Type = %q", m.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("committee member did not receive the vote trigger")
	}
}

// stripProof keeps every identifying field and drops only the proof bytes.
func TestStripProof(t *testing.T) {
	blk := testBlock(3)
	cp := stripProof(blk)
	if cp == blk {
		t.Fatal("stripProof must return a copy")
	}
	if cp.StarkProof != nil || len(blk.StarkProof) != 1024 {
		t.Fatal("stripProof must clear only the copy's proof")
	}
	if cp.BlockHash != blk.BlockHash || cp.ConsensusHashHex() != blk.ConsensusHashHex() || cp.BlockNumber != blk.BlockNumber {
		t.Fatal("stripProof changed identifying fields")
	}
	if stripProof(nil) != nil {
		t.Fatal("stripProof(nil) must be nil")
	}
}

// finalizeCommitteeTargets = connected peers that voted (certificate) or sit on
// the cached committee; everyone else is left to gossip.
func TestFinalizeCommitteeTargets(t *testing.T) {
	seq := newLoopbackHost(t)
	voter := newLoopbackHost(t)
	other := newLoopbackHost(t)
	connect(t, seq, voter)
	connect(t, seq, other)
	blk := testBlock(9)
	bls := []BLS_Signer.BLSresponse{{PeerID: voter.ID().String(), Agree: true}}
	targets := finalizeCommitteeTargets(seq, blk, bls)
	if len(targets) != 1 || targets[0] != voter.ID() {
		t.Fatalf("targets = %v, want only the voter", targets)
	}
}

// A zkblock_cert envelope for a block this node never saw as a proposal is
// ignored (no apply, no forward) — the gossiped full copy is the fallback.
func TestHandleReceivedBlockMessage_CertOnlyWithoutProposalIsIgnored(t *testing.T) {
	blk := stripProof(testBlock(11))
	blk.BlockHash = common.BigToHash(common.Big257) // nothing cached under this hash
	msg := config.BlockMessage{
		Sender: "seq", Nonce: "n", Block: blk, Type: blockCertMessageType,
		Data: map[string]string{"block_hash": blk.BlockHash.Hex(), "status": "verified"},
	}
	// Must not panic and must not mark the block as processed.
	HandleReceivedBlockMessage(msg, "seq", true)
	full := msg
	full.Type = "zkblock"
	if isMessageProcessed(getBlockDedupID(full)) {
		t.Fatal("cert-only envelope without a cached proposal must not enter the dedup cache")
	}
}
