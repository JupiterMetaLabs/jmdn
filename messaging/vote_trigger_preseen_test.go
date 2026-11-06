package messaging

// A vote trigger's ID is predictable: sha256(sender, fixed text, unix second).
// The receive path used to mark a message seen BEFORE checking that a vote
// trigger came from the pinned sequencer, so any peer could send triggers
// naming the sequencer, with Hops > 0 (the envelope's origin check applies only
// at Hops == 0) and the IDs of the coming seconds. Each was marked seen and then
// dropped as unauthorized, and the real trigger for that second was then
// dropped as a duplicate: no validator voted.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"gossipnode/config"
	"gossipnode/internal/saferun"
	"gossipnode/metrics"
)

const voteTriggerContent = "Vote trigger broadcast - initiate voting process"

// voteTrigger builds a trigger exactly as BroadcastVoteTrigger does, for the
// given sender, second and proposal.
func voteTrigger(sender string, ts int64, hops int, data string) BroadcastMessageStruct {
	m := BroadcastMessageStruct{
		Sender: sender, Content: voteTriggerContent, Timestamp: ts,
		Hops: hops, Type: voteTriggerBroadcastType, Data: data,
	}
	m.ID = generateMessageID(m.Sender, m.Content, m.Timestamp)
	return m
}

// The forged trigger passes the envelope (that is the attack's premise), is
// refused by the sender check without being marked seen, and the real trigger
// for the same second is then processed.
func TestVoteTrigger_ForgedTriggerCannotPreMarkTheRealOne(t *testing.T) {
	seq, attacker := newLoopbackHost(t), newLoopbackHost(t)
	withPinnedSequencer(t, seq.ID().String())
	ts := time.Now().Add(30 * time.Second).Unix() // a second still to come

	forged := voteTrigger(seq.ID().String(), ts, 1, `{"forged":true}`)
	if reason := checkBroadcastEnvelope(&forged, attacker.ID().String(), time.Now().UTC()); reason != "" {
		t.Fatalf("test premise: the forged trigger must pass the envelope, got %q", reason)
	}
	if got := admitBroadcast(&forged, attacker.ID()); got != "vote_trigger_unauthorized_sender" {
		t.Fatalf("forged trigger from a non-sequencer peer: got %q", got)
	}

	real := voteTrigger(seq.ID().String(), ts, config.MaxHops, `{"real":true}`)
	if real.ID != forged.ID {
		t.Fatal("test premise: the forged trigger must carry the real trigger's ID")
	}
	if got := admitBroadcast(&real, seq.ID()); got != "" {
		t.Fatalf("the real trigger was not processed after a forged one with its ID: %q", got)
	}
	if got := admitBroadcast(&real, seq.ID()); got != "duplicate" {
		t.Fatalf("a second copy of the real trigger must be a duplicate, got %q", got)
	}
}

// With no pinned sequencer every sender is accepted (the known unpinned case),
// but a forged trigger with the real ID and another proposal still cannot
// shadow the real one: the dedup key covers the proposal.
func TestVoteTrigger_UnpinnedForgedProposalDoesNotShadowTheRealOne(t *testing.T) {
	seq, attacker := newLoopbackHost(t), newLoopbackHost(t)
	withPinnedSequencer(t, "")
	ts := time.Now().Add(31 * time.Second).Unix()

	forged := voteTrigger(seq.ID().String(), ts, 1, `{"forged":true}`)
	if got := admitBroadcast(&forged, attacker.ID()); got != "" {
		t.Fatalf("unpinned: got %q", got)
	}
	real := voteTrigger(seq.ID().String(), ts, config.MaxHops, `{"real":true}`)
	if got := admitBroadcast(&real, seq.ID()); got != "" {
		t.Fatalf("unpinned: the real trigger was shadowed by a forged one with its ID: %q", got)
	}
}

// The sequencer marks its own trigger with the same key it would compute on
// receipt, so a copy that comes back is still a duplicate.
func TestVoteTrigger_SenderMarksTheSameKey(t *testing.T) {
	seq := newLoopbackHost(t)
	withPinnedSequencer(t, seq.ID().String())
	own := voteTrigger(seq.ID().String(), time.Now().Unix(), config.MaxHops, `{"own":true}`)
	markMessageSeen(broadcastSeenKey(&own))
	if got := admitBroadcast(&own, seq.ID()); got != "duplicate" {
		t.Fatalf("own trigger received back: got %q, want duplicate", got)
	}
}

// Other broadcast types keep their ID as the dedup key (their ID already binds
// the payload), so nothing changes for them.
func TestBroadcastSeenKey_OtherTypesUseTheID(t *testing.T) {
	m := envelopeMsg("A", "operator_note", 0, time.Now().Unix())
	if broadcastSeenKey(&m) != m.ID {
		t.Fatal("a non-vote-trigger broadcast must dedup on its ID")
	}
	a := voteTrigger("S", 1, 0, "x")
	b := voteTrigger("S", 1, 0, "y")
	if a.ID != b.ID || broadcastSeenKey(&a) == broadcastSeenKey(&b) {
		t.Fatal("two triggers with one ID and different proposals must have different dedup keys")
	}
}

// End to end over libp2p with the production handler: the attacker sends a
// burst of forged triggers for the coming seconds; none is marked seen, so the
// real trigger for any of those seconds is still processed.
func TestBroadcastStream_ForgedTriggerBurstMarksNothing(t *testing.T) {
	seq, attacker, receiver := newLoopbackHost(t), newLoopbackHost(t), newLoopbackHost(t)
	withPinnedSequencer(t, seq.ID().String())
	receiver.SetStreamHandler(config.BroadcastProtocol, saferun.Stream(string(config.BroadcastProtocol), HandleBroadcastStream))
	connect(t, attacker, receiver)

	counter := metrics.BlocksRejectedCounter.WithLabelValues("vote_trigger_unauthorized_sender", "remote")
	before := testutil.ToFloat64(counter)
	base := time.Now().Add(5 * time.Second).Unix()
	const burst = 5
	for i := int64(0); i < burst; i++ {
		frame, _ := json.Marshal(voteTrigger(seq.ID().String(), base+i, 1, `{"forged":true}`))
		s, err := attacker.NewStream(context.Background(), receiver.ID(), config.BroadcastProtocol)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = s.Write(append(frame, '\n'))
		_ = s.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for testutil.ToFloat64(counter)-before < burst {
		if time.Now().After(deadline) {
			t.Fatalf("only %v of %d forged triggers were refused as unauthorized", testutil.ToFloat64(counter)-before, burst)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := int64(0); i < burst; i++ {
		real := voteTrigger(seq.ID().String(), base+i, config.MaxHops, `{"real":true}`)
		if isMessageSeen(real.ID) || isMessageSeen(broadcastSeenKey(&real)) {
			t.Fatalf("second %d: a forged trigger left the real trigger's ID marked seen", base+i)
		}
	}
}
