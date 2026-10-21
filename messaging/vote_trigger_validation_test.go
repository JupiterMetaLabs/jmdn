package messaging

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/internal/saferun"
	"gossipnode/metrics"
)

func withPinnedSequencer(t *testing.T, pin string) {
	t.Helper()
	prev := voteTriggerPinnedSequencer
	voteTriggerPinnedSequencer = func() string { return pin }
	t.Cleanup(func() { voteTriggerPinnedSequencer = prev })
}

// A vote trigger whose consensus message carries no block is dropped.
func TestVoteTrigger_NilBlockIsDropped(t *testing.T) {
	withPinnedSequencer(t, "")
	// A listener node is set, as on every live node — otherwise the handler
	// returns before reaching SubmitVote and the test would not exercise it.
	gv := PubSubMessages.NewGlobalVariables()
	prev := gv.Get_ForListner()
	gv.Set_ForListner(&PubSubMessages.BuddyNode{})
	t.Cleanup(func() { gv.Set_ForListner(prev) })
	before := testutil.ToFloat64(metrics.BlocksRejectedCounter.WithLabelValues("vote_trigger_malformed", "remote"))
	handleVoteTriggerBroadcast(BroadcastMessageStruct{
		ID: "x", Type: voteTriggerBroadcastType, Data: `{"ZKBlock":null}`,
	}, peer.ID(""))
	if testutil.ToFloat64(metrics.BlocksRejectedCounter.WithLabelValues("vote_trigger_malformed", "remote"))-before != 1 {
		t.Fatal("nil-block vote trigger was not rejected as malformed")
	}
}

// A sender-less transaction inside the proposal is also malformed.
func TestValidateVoteTrigger_RejectsSenderlessTx(t *testing.T) {
	cm := PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(&config.ZKBlock{
		BlockHash:    common.HexToHash("0xab"),
		Transactions: []config.Transaction{{Hash: common.HexToHash("0x01")}},
	})
	if err := validateVoteTriggerConsensusMessage(cm); err == nil {
		t.Fatal("want rejection of a transaction with no From")
	}
	if err := validateVoteTriggerConsensusMessage(PubSubMessages.NewConsensusMessageBuilder(nil).SetZKBlock(&config.ZKBlock{})); err == nil {
		t.Fatal("want rejection of a block with no hash")
	}
}

// With a pinned sequencer, a vote trigger from any other (authenticated) peer
// is dropped before it is parsed or cached.
func TestVoteTrigger_UnpinnedSenderDropped(t *testing.T) {
	seq := newLoopbackHost(t)
	other := newLoopbackHost(t)
	withPinnedSequencer(t, seq.ID().String())

	before := testutil.ToFloat64(metrics.BlocksRejectedCounter.WithLabelValues("vote_trigger_unauthorized_sender", "remote"))
	handleVoteTriggerBroadcast(BroadcastMessageStruct{ID: "y", Type: voteTriggerBroadcastType, Data: `{"ZKBlock":null}`}, other.ID())
	if testutil.ToFloat64(metrics.BlocksRejectedCounter.WithLabelValues("vote_trigger_unauthorized_sender", "remote"))-before != 1 {
		t.Fatal("vote trigger from a non-sequencer peer was not rejected")
	}
	if ok, enforced := voteTriggerSenderAuthorized(seq.ID()); !ok || !enforced {
		t.Fatalf("pinned sequencer must be authorized (ok=%v enforced=%v)", ok, enforced)
	}
}

// End to end over libp2p with the production wrapper: a malformed frame sent
// to /broadcast must leave the receiving host alive and serving.
func TestBroadcastStream_MalformedFrameLeavesNodeServing(t *testing.T) {
	withPinnedSequencer(t, "")
	sender := newLoopbackHost(t)
	receiver := newLoopbackHost(t)
	receiver.SetStreamHandler(config.BroadcastProtocol, saferun.Stream(string(config.BroadcastProtocol), HandleBroadcastStream))
	connect(t, sender, receiver)

	frame, _ := json.Marshal(BroadcastMessageStruct{ID: "malformed-" + time.Now().String(), Type: voteTriggerBroadcastType, Data: `{"ZKBlock":null}`})
	for i := 0; i < 2; i++ {
		s, err := sender.NewStream(context.Background(), receiver.ID(), config.BroadcastProtocol)
		if err != nil {
			t.Fatalf("stream %d: receiver no longer serving: %v", i, err)
		}
		_, _ = s.Write(append(frame, '\n'))
		_ = s.Close()
		time.Sleep(100 * time.Millisecond)
	}
}
