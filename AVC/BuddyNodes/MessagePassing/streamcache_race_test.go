package MessagePassing

import (
	"sync"
	"testing"
	"time"

	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
)

// Concurrent misses for one peer must leave exactly one cached stream and one
// LRU entry; the losing dials are closed, not leaked.
func TestGetStream_ConcurrentMissesCacheOneStream(t *testing.T) {
	mn := mocknet.New()
	defer mn.Close()
	a, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	b, err := mn.GenPeer()
	if err != nil {
		t.Fatal(err)
	}
	if err := mn.LinkAll(); err != nil {
		t.Fatal(err)
	}
	if err := mn.ConnectAllButSelf(); err != nil {
		t.Fatal(err)
	}
	b.SetStreamHandler(config.BuddyNodesMessageProtocol, func(s network.Stream) {
		time.Sleep(50 * time.Millisecond)
		_ = s.Close()
	})

	sc := NewStreamCacheBuilder(&AVCStruct.StreamCache{
		Streams:     make(map[peer.ID]*AVCStruct.StreamEntry),
		AccessOrder: []peer.ID{},
		MaxStreams:  10,
		TTL:         time.Minute,
		Host:        a,
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := sc.GetStream(b.ID()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if n := len(sc.StreamCache.Streams); n != 1 {
		t.Fatalf("cached streams = %d, want 1", n)
	}
	if n := len(sc.StreamCache.AccessOrder); n != 1 {
		t.Fatalf("LRU order entries = %d, want 1", n)
	}
}
