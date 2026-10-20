package messaging

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"gossipnode/config"
	"gossipnode/metrics"
)

func withPinnedSequencerID(t *testing.T, pin string) {
	t.Helper()
	prev := blockSenderPin
	blockSenderPin = func() string { return pin }
	t.Cleanup(func() { blockSenderPin = prev })
}

// With a pinned sequencer, only it may publish or directly send finalized
// blocks; with no pin everything is accepted (and the caller warns).
func TestAuthorizeBlockSender(t *testing.T) {
	seq := newLoopbackHost(t)
	other := newLoopbackHost(t)

	withPinnedSequencerID(t, "")
	if ok, enforced := AuthorizeBlockSender(other.ID().String()); !ok || enforced {
		t.Fatalf("no pin: want accept/unenforced, got ok=%v enforced=%v", ok, enforced)
	}

	withPinnedSequencerID(t, seq.ID().String())
	if ok, enforced := AuthorizeBlockSender(seq.ID().String()); !ok || !enforced {
		t.Fatalf("pinned sequencer must be accepted: ok=%v enforced=%v", ok, enforced)
	}
	if ok, _ := AuthorizeBlockSender(other.ID().String()); ok {
		t.Fatal("non-sequencer must be rejected when pinned")
	}
	if ok, _ := AuthorizeBlockSender(""); ok {
		t.Fatal("empty sender must be rejected when pinned")
	}
}

// A gossiped block whose publisher is not the pinned sequencer never reaches
// the apply path.
func TestHandleGossipedBlockMessageFrom_RejectsNonSequencerPublisher(t *testing.T) {
	seq := newLoopbackHost(t)
	other := newLoopbackHost(t)
	withPinnedSequencerID(t, seq.ID().String())

	before := testutil.ToFloat64(metrics.BlocksRejectedCounter.WithLabelValues("publisher_not_sequencer", "gossip"))
	blk := testBlock(77)
	HandleGossipedBlockMessageFrom(config.BlockMessage{Sender: "x", Nonce: "n", Block: blk, Type: "zkblock"}, other.ID())
	if testutil.ToFloat64(metrics.BlocksRejectedCounter.WithLabelValues("publisher_not_sequencer", "gossip"))-before != 1 {
		t.Fatal("block from a non-sequencer publisher was not rejected")
	}
}
