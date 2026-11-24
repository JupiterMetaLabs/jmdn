package messaging

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/config"
)

// TestBroadcastWriteDeadline_PeerThatNeverReads reproduces the 2026-10-13
// testnet wedge: the sequencer's vote-trigger broadcast opened a stream to a
// peer whose receive side had stopped draining, the ~2.6 MB Write parked in
// yamux flow control with no deadline, and BroadcastVoteTrigger's WaitGroup
// never returned — three consecutive rounds hung at Consensus.go:1112.
//
// The receiver here accepts the stream and never reads. The write must fail
// within broadcastWriteTimeout instead of blocking forever.
func TestBroadcastWriteDeadline_PeerThatNeverReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	sender, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("sender host: %v", err)
	}
	defer sender.Close()
	receiver, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("receiver host: %v", err)
	}
	defer receiver.Close()

	accepted := make(chan struct{})
	receiver.SetStreamHandler(config.BroadcastProtocol, func(s network.Stream) {
		close(accepted)
		<-ctx.Done() // never read, never close: receive window fills and stays full
		_ = s.Reset()
	})

	if err := sender.Connect(ctx, peer.AddrInfo{ID: receiver.ID(), Addrs: receiver.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Larger than any muxer receive window (yamux default 256 KiB; libp2p may
	// raise it) so the write cannot complete without the peer draining.
	payload := bytes.Repeat([]byte{'x'}, 6*1024*1024)

	stream, err := sender.NewStream(ctx, receiver.ID(), config.BroadcastProtocol)
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	defer stream.Close()

	const deadline = 3 * time.Second
	_ = stream.SetWriteDeadline(time.Now().Add(deadline))

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := stream.Write(payload)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("write of %d bytes to a non-reading peer succeeded; receive window larger than payload?", len(payload))
		}
		if elapsed := time.Since(start); elapsed > deadline+5*time.Second {
			t.Fatalf("write returned after %s, expected ~%s", elapsed, deadline)
		}
		t.Logf("write failed as expected after %s: %v", time.Since(start), err)
	case <-time.After(deadline + 15*time.Second):
		t.Fatalf("write still blocked %s after a %s write deadline — deadline not honoured", deadline+15*time.Second, deadline)
	}

	select {
	case <-accepted:
	default:
		t.Fatalf("receiver never accepted the stream")
	}
}

// TestBroadcastWriteTimeoutIsBounded guards the constant: it must be shorter
// than the consensus timeout so a wedged peer costs one broadcast slot, not
// the round.
func TestBroadcastWriteTimeoutIsBounded(t *testing.T) {
	if broadcastWriteTimeout <= 0 || broadcastWriteTimeout >= config.ConsensusTimeout {
		t.Fatalf("broadcastWriteTimeout=%s must be in (0, ConsensusTimeout=%s)", broadcastWriteTimeout, config.ConsensusTimeout)
	}
}
