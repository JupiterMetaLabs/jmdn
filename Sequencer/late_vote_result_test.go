package Sequencer

import (
	"bufio"
	"encoding/json"
	"testing"
	"time"

	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
)

// serveVoteResult makes h answer one vote-result request with a signed-looking
// result after delay, then reports on done whether the write went through.
func serveVoteResult(t *testing.T, h host.Host, delay time.Duration, done chan<- error) {
	t.Helper()
	h.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		defer s.Close()
		if _, err := bufio.NewReader(s).ReadString(byte(config.Delimiter)); err != nil {
			done <- err
			return
		}
		time.Sleep(delay)
		body, _ := json.Marshal(map[string]interface{}{
			"result": 1,
			"bls": map[string]interface{}{
				"Signature": "aa", "Agree": true, "PubKey": "bb", "PeerID": h.ID().String(),
			},
		})
		msg := PubSubMessages.NewMessageBuilder(nil).
			SetSender(h.ID()).
			SetMessage(string(body)).
			SetTimestamp(time.Now().UTC().Unix())
		raw, _ := json.Marshal(msg)
		_, err := s.Write(append(raw, byte(config.Delimiter)))
		done <- err
	})
}

// A vote result that arrives after the wait for it has ended is never used:
// the round is decided on the results that arrived in time, and nothing is
// added to them afterwards.
func TestLateVoteResultIsDropped(t *testing.T) {
	orig := voteResultReadTimeout
	voteResultReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { voteResultReadTimeout = orig })

	mn := mocknet.New()
	t.Cleanup(func() { _ = mn.Close() })
	seq, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	onTime, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	late, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	if err := mn.LinkAll(); err != nil {
		t.Fatal(err)
	}
	if err := mn.ConnectAllButSelf(); err != nil {
		t.Fatal(err)
	}

	onTimeDone := make(chan error, 1)
	lateDone := make(chan error, 1)
	serveVoteResult(t, onTime, 0, onTimeDone)
	serveVoteResult(t, late, 3*voteResultReadTimeout, lateDone)

	block := &config.ZKBlock{BlockNumber: 70, BlockHash: common.HexToHash("0x70")}
	c := &Consensus{
		Host:        seq,
		ZKBlockData: PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(block),
	}
	listener := &PubSubMessages.BuddyNode{}
	listener.BuddyNodes.Buddies_Nodes = []peer.ID{onTime.ID(), late.ID()}

	start := time.Now()
	results := c.CollectVoteResultsFromBuddies(listener)
	if took := time.Since(start); took >= 3*voteResultReadTimeout {
		t.Fatalf("collection waited %v for the late member; it should stop at the timeout", took)
	}
	if len(results) != 1 || results[0].PeerID != onTime.ID().String() {
		t.Fatalf("results %+v, want only the on-time member's", results)
	}

	// The late member was asked and did answer, only after collection had
	// ended: its result was dropped for being late, not lost on the way.
	select {
	case <-lateDone:
	case <-time.After(10 * voteResultReadTimeout):
		t.Fatal("late member never answered")
	}
	if err := <-onTimeDone; err != nil {
		t.Fatalf("on-time member's answer failed: %v", err)
	}
}
