package MessagePassing

import (
	"context"
	"testing"
	"time"

	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestGivenSubmitStreamCached_WhenClosed_ThenEvictedFromCache(t *testing.T) {
	// Given: two connected libp2p hosts
	serverHost, err := libp2p.New()
	if err != nil {
		t.Fatalf("failed to create server host: %v", err)
	}
	defer serverHost.Close()

	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatalf("failed to create client host: %v", err)
	}
	defer clientHost.Close()

	serverHost.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		// We only need the stream to be accepted and remain open long enough to be cached.
		// Close after a short delay to avoid leaking goroutines in the test.
		time.Sleep(25 * time.Millisecond)
		_ = s.Close()
	})

	if err := clientHost.Connect(context.Background(), peer.AddrInfo{
		ID:    serverHost.ID(),
		Addrs: serverHost.Addrs(),
	}); err != nil {
		t.Fatalf("failed to connect client to server: %v", err)
	}

	streamCache := &AVCStruct.StreamCache{
		Streams:     make(map[peer.ID]*AVCStruct.StreamEntry),
		AccessOrder: make([]peer.ID, 0),
		MaxStreams:  10,
		TTL:         5 * time.Minute,
		Host:        clientHost,
	}

	sc := NewStreamCacheBuilder(streamCache)
	if sc == nil {
		t.Fatalf("expected stream cache builder, got nil")
	}

	// When: create a submit stream and then close it via the dedicated submit close function
	_, err = sc.GetSubmitMessageStream(serverHost.ID())
	if err != nil {
		t.Fatalf("failed to create submit stream: %v", err)
	}

	if _, ok := sc.StreamCache.Streams[submitMessageStreamKey(serverHost.ID())]; !ok {
		t.Fatalf("expected submit stream to be cached under submit key")
	}

	sc.CloseSubmitMessageStream(serverHost.ID())

	// Then: submit stream entry is evicted
	if _, ok := sc.StreamCache.Streams[submitMessageStreamKey(serverHost.ID())]; ok {
		t.Fatalf("expected submit stream to be evicted from cache")
	}
}
