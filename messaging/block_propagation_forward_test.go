package messaging

// forwardBlock regression tests:
//   - an unvalidated, non-zkblock message type must never be re-flooded to
//     peers — that was free amplification for anyone, authenticated or not.
//   - forwardBlock must not hang forever on a peer that accepts the stream
//     and never reads.
//   - forwardBlock must not share a WaitGroup with sendBlockMessageDirect:
//     a stuck relay peer must never stall the sequencer's own direct send.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

func newConnectedPair(t *testing.T) (a, b host.Host) {
	t.Helper()
	ha, err := libp2p.New()
	if err != nil {
		t.Fatalf("host a: %v", err)
	}
	t.Cleanup(func() { _ = ha.Close() })
	hb, err := libp2p.New()
	if err != nil {
		t.Fatalf("host b: %v", err)
	}
	t.Cleanup(func() { _ = hb.Close() })
	ha.Peerstore().AddAddrs(hb.ID(), hb.Addrs(), time.Hour)
	if err := ha.Connect(t.Context(), peer.AddrInfo{ID: hb.ID(), Addrs: hb.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return ha, hb
}

// oversizedPayloadMsg builds a block message with a multi-MB Data field —
// comfortably larger than a libp2p stream's local send buffer (yamux's
// default per-stream window is in the low hundreds of KB), so the write
// genuinely cannot complete without the remote side reading, instead of
// completing instantly from being buffered locally.
func oversizedPayloadMsg(id string) config.BlockMessage {
	const oversized = 4 << 20 // 4 MiB — well past any stream-level send buffer.
	return config.BlockMessage{
		ID:     id,
		Type:   "zkblock",
		Sender: "someone-else",
		Block:  &config.ZKBlock{BlockNumber: 1},
		Data:   map[string]string{"padding": strings.Repeat("x", oversized)},
	}
}

// TestHandleReceivedBlockMessage_UnvalidatedTypeIsNeverForwarded is the
// decisive regression test: a junk, non-zkblock message type must never
// reach forwardBlock, verified over a real connection — not by reading the
// source, by actually confirming the peer never receives it.
func TestHandleReceivedBlockMessage_UnvalidatedTypeIsNeverForwarded(t *testing.T) {
	self, peerHost := newConnectedPair(t)

	var hits int32
	peerHost.SetStreamHandler(config.BlockPropagationProtocol, func(s network.Stream) {
		atomic.AddInt32(&hits, 1)
		_ = s.Close()
	})

	prevHost := globalHost
	SetHostInstance(self)
	t.Cleanup(func() { globalHost = prevHost })

	msg := config.BlockMessage{
		ID:     "junk-type-1",
		Type:   "definitely_not_zkblock_or_cert",
		Sender: self.ID().String(),
		Hops:   0,
		Data:   map[string]string{},
	}
	HandleReceivedBlockMessage(msg, "test-remote", true)

	// Give any (incorrect) forwarding goroutine time to land before asserting
	// it didn't.
	time.Sleep(300 * time.Millisecond)
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("unvalidated message type was forwarded to a peer, got %d hits, want 0", n)
	}
}

// TestForwardBlock_StalledPeerDoesNotHangTheCall proves forwardBlock returns
// within its own write-deadline budget when a peer accepts the stream and
// never reads, instead of blocking indefinitely. The payload is large enough
// that the write cannot complete without the remote draining it — a small
// payload fits in the stream's local buffer and returns instantly whether or
// not the deadline fix is in place, which would make this assertion
// meaningless.
func TestForwardBlock_StalledPeerDoesNotHangTheCall(t *testing.T) {
	self, stalledPeer := newConnectedPair(t)

	stalledPeer.SetStreamHandler(config.BlockPropagationProtocol, func(s network.Stream) {
		// Accept the stream and never read or close it — the exact stalled-peer
		// shape this test is about.
	})

	msg := oversizedPayloadMsg("stall-1")

	done := make(chan struct{})
	start := time.Now()
	go func() {
		forwardBlock(self, msg)
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > broadcastWriteTimeout+10*time.Second {
			t.Fatalf("forwardBlock took %v, expected to return within its write-deadline budget", elapsed)
		}
	case <-time.After(broadcastWriteTimeout + 15*time.Second):
		t.Fatal("forwardBlock hung well past its write-deadline budget — stalled peer blocked the call")
	}
}

// TestForwardBlock_DoesNotShareWaitGroupWithSendBlockMessageDirect proves the
// two senders no longer share GRO.BlockPropagationForwardWG: a forwardBlock
// call stuck on a stalled peer must not delay a concurrent
// sendBlockMessageDirect call to a healthy peer.
func TestForwardBlock_DoesNotShareWaitGroupWithSendBlockMessageDirect(t *testing.T) {
	self, stalledPeer := newConnectedPair(t)
	_, healthyPeer := newConnectedPair(t)
	self.Peerstore().AddAddrs(healthyPeer.ID(), healthyPeer.Addrs(), time.Hour)
	if err := self.Connect(t.Context(), peer.AddrInfo{ID: healthyPeer.ID(), Addrs: healthyPeer.Addrs()}); err != nil {
		t.Fatalf("connect self->healthy: %v", err)
	}

	stalledPeer.SetStreamHandler(config.BlockPropagationProtocol, func(s network.Stream) {
		// Never read — stalls forwardBlock's write to this peer.
	})
	healthyPeer.SetStreamHandler(config.BlockPropagationProtocol, func(s network.Stream) {
		buf := make([]byte, 4096)
		for {
			if _, err := s.Read(buf); err != nil {
				break
			}
		}
		_ = s.Close()
	})

	go forwardBlock(self, oversizedPayloadMsg("wg-stall")) // deliberately not awaited: the stuck relay runs in the background

	time.Sleep(200 * time.Millisecond) // let the stalled call actually start blocking on the write

	directMsg := config.BlockMessage{ID: "wg-direct", Type: "zkblock", Sender: "x", Block: &config.ZKBlock{BlockNumber: 2}, Data: map[string]string{}}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- sendBlockMessageDirect(self, directMsg, []peer.ID{healthyPeer.ID()}, "test") }()

	select {
	case err := <-done:
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("sendBlockMessageDirect took %v while a forwardBlock call was stuck on a different peer — WaitGroups are still shared", elapsed)
		}
		if err != nil {
			t.Fatalf("sendBlockMessageDirect: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sendBlockMessageDirect never returned — likely still blocked behind forwardBlock's stuck peer")
	}
}

// With block gossip off, a received block is forwarded on one goroutine
// (forwardBlock serializes it) while another sets CommitteeCertificate on it
// before apply (prepareReceivedBlock). blockMessageForForward gives the forward
// its own copy, so the two never share what is written. Run with -race.
func TestBlockMessageForForward_ApplyWritesDoNotReachTheForwardedCopy(t *testing.T) {
	to := common.HexToAddress("0x00000000000000000000000000000000000000f0")
	msg := config.BlockMessage{
		Type: "zkblock",
		Data: map[string]string{},
		Block: &config.ZKBlock{
			BlockNumber:  9,
			BlockHash:    common.HexToHash("0xf0"),
			Transactions: []config.Transaction{{Type: 2, To: &to}},
		},
	}
	fwd := blockMessageForForward(msg)
	if fwd.Block == msg.Block {
		t.Fatal("the forwarded message shares the apply goroutine's block")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // forwardBlock
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := json.Marshal(fwd); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() { // prepareReceivedBlock and apply
		defer wg.Done()
		for i := 0; i < 200; i++ {
			msg.Block.CommitteeCertificate = fmt.Sprint(i)
			msg.Block.StateFingerprint = fmt.Sprint(i)
		}
	}()
	wg.Wait()

	if fwd.Block.CommitteeCertificate != "" || fwd.Block.StateFingerprint != "" {
		t.Fatal("a write on the apply side reached the forwarded copy")
	}
	if fwd.Block.BlockHash != msg.Block.BlockHash || len(fwd.Block.Transactions) != 1 {
		t.Fatal("the forwarded copy lost block content")
	}
	if got := blockMessageForForward(config.BlockMessage{}); got.Block != nil {
		t.Fatal("a message with no block must stay without one")
	}
}

// TestHandleReceivedBlockMessage_DuplicateDoesNotTimeOutSender: every block
// arrives twice by design (gossip + direct), so the second copy must not time
// its sender out. It used to (20 s), which shut the sequencer's own direct
// channel whenever the gossip copy won the race.
func TestHandleReceivedBlockMessage_DuplicateDoesNotTimeOutSender(t *testing.T) {
	const sender = "dup-sender-peer"
	t.Cleanup(func() {
		peerTimeoutMutex.Lock()
		delete(peerTimeouts, sender)
		peerTimeoutMutex.Unlock()
	})

	msg := config.BlockMessage{
		ID:     "dup-1",
		Type:   "zkblock",
		Sender: sender,
		Block:  &config.ZKBlock{BlockNumber: 424242, Timestamp: time.Now().UnixNano()},
	}
	markMessageProcessed(getBlockDedupID(msg)) // the first copy was already applied

	HandleReceivedBlockMessage(msg, sender, false)

	if isPeerTimedOut(sender) {
		t.Fatal("sender was timed out for delivering a duplicate block; its next block on the direct channel would be dropped")
	}
}
