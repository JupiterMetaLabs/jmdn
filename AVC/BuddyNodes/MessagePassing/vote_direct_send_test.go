package MessagePassing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendMessageToPeer_VoteDeliveredIntactBeforeClose closes out the
// CloseWrite()/stream-close investigation: once "ack" (not "ACK") is read
// correctly, isVote resolves true and SendMessageToPeer returns right after
// stream.Write, with no read-response wait — the deferred stream.Close()
// fires almost immediately afterward. MuxedStream.Close() flushes buffered
// writes but documents that it "does not guarantee receipt of the data."
// This test checks, against real libp2p hosts (not just the doc comment),
// whether the receiver reliably gets the complete, unmangled vote message
// every time under that immediate-close timing, across many iterations —
// a single pass could pass by luck.
func TestSendMessageToPeer_VoteDeliveredIntactBeforeClose(t *testing.T) {
	if testing.Short() {
		t.Skip("excluded from -short: brings up libp2p hosts and needs full node settings")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1, err := libp2p.New()
	require.NoError(t, err)
	defer h1.Close()

	h2, err := libp2p.New()
	require.NoError(t, err)
	defer h2.Close()

	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	require.NoError(t, h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}))

	bn := &AVCStruct.BuddyNode{
		Host: h1,
		StreamCache: &AVCStruct.StreamCache{
			Host:        h1,
			Streams:     make(map[peer.ID]*AVCStruct.StreamEntry),
			MaxStreams:  100,
			TTL:         time.Hour,
			AccessOrder: []peer.ID{},
		},
	}
	sl := NewListenerStruct(bn)

	type received struct {
		msg *AVCStruct.Message
		err error
	}
	resultCh := make(chan received, 1)

	// Receiver: a raw read of the stream, independent of the full production
	// HandleSubmitMessageStream/handleSubmitVote chain (which needs a live
	// listener node, CRDT layer and global state wired up). This test's
	// question is narrower and more fundamental than any of that: do the
	// exact bytes written by SendMessageToPeer arrive intact at all, given
	// the write is immediately followed by stream.Close() on the sender
	// side. Reading with the same delimiter-framed bufio.Reader the real
	// handler uses is enough to answer that.
	h2.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		defer s.Close()
		reader := bufio.NewReader(s)
		raw, readErr := reader.ReadString(config.Delimiter)
		if readErr != nil {
			resultCh <- received{nil, readErr}
			return
		}
		msg := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(raw)
		if msg == nil {
			resultCh <- received{nil, fmt.Errorf("failed to unmarshal received message: %q", raw)}
			return
		}
		resultCh <- received{msg, nil}
	})

	iterations := 50
	for i := 0; i < iterations; i++ {
		votePayload := fmt.Sprintf("vote-payload-%d", i)

		// Built exactly as Vote/Trigger.go builds a real vote message
		// (SetACK with True_ACK_Message, Stage = Type_SubmitVote), so this
		// exercises the real isVote==true classification in
		// SendMessageToPeer, not a hand-rolled approximation of it.
		voteMessage := AVCStruct.NewMessageBuilder(nil).
			SetSender(h1.ID()).
			SetMessage(votePayload).
			SetTimestamp(time.Now().UTC().Unix()).
			SetACK(AVCStruct.NewACKBuilder().True_ACK_Message(h1.ID(), config.Type_SubmitVote))

		raw, err := json.Marshal(voteMessage)
		require.NoError(t, err)

		sendErr := sl.SendMessageToPeer(ctx, h2.ID(), string(raw))
		assert.NoErrorf(t, sendErr, "iteration %d: SendMessageToPeer returned an error for a real vote message — isVote should have been true, skipping the response wait entirely", i)

		select {
		case got := <-resultCh:
			if assert.NoErrorf(t, got.err, "iteration %d: receiver failed to read the vote message off the stream", i) {
				assert.Equalf(t, votePayload, got.msg.Message, "iteration %d: receiver's message field does not match what was sent — truncation or corruption around stream close", i)
				assert.Equalf(t, config.Type_SubmitVote, got.msg.GetACK().GetStage(), "iteration %d: receiver's ACK stage does not match — message was not delivered intact", i)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("iteration %d: receiver never got a complete message within 2s — looks like the sender's immediate stream.Close() can race ahead of delivery", i)
		}
	}
}
