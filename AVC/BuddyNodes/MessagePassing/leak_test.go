package MessagePassing

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"jmdn/config"
	AVCStruct "jmdn/config/PubSubMessages"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
)

// TestStreamLeak verifies that streams are properly closed when read errors occur
// This simulates the scenario where a peer hangs and causes a read timeout
func TestStreamLeak(t *testing.T) {
	// 1. Setup two libp2p hosts
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1, err := libp2p.New()
	assert.NoError(t, err)
	defer h1.Close()

	h2, err := libp2p.New()
	assert.NoError(t, err)
	defer h2.Close()

	// 2. Connect h1 to h2
	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	err = h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	assert.NoError(t, err)

	// 3. Setup h2 to accept streams and HANG (timeout simulation)
	h2.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		// Read the request to clear buffer/window
		buf := make([]byte, 1024)
		s.Read(buf)
		// Hang longer than the sender's timeout (100ms)
		time.Sleep(300 * time.Millisecond)
		// Close on receiver side eventually
		s.Close()
	})

	// 4. Setup StructListener on h1
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

	// Helper to get FD count
	getFDCount := func() int {
		pid := os.Getpid()
		out, err := exec.Command("sh", "-c", fmt.Sprintf("lsof -p %d | wc -l", pid)).Output()
		if err != nil {
			return 0
		}
		val, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		return val
	}

	initialFDs := getFDCount()
	fmt.Printf("Initial File Descriptors: %d\n", initialFDs)

	// 5. Run the loop to trigger leaks
	iterations := 50

	fmt.Printf("Starting leak test with %d iterations (Timeout simulation)...\n", iterations)

	for i := 0; i < iterations; i++ {
		// Send a dummy subscription request
		msg := `{"type": "subscription_request"}`
		err := sl.SendMessageToPeer(ctx, h2.ID(), msg)

		// Assert that we got an error (deadline exceeded)
		assert.Error(t, err, "Expected error from SendMessageToPeer due to timeout")
		if err != nil {
			assert.Contains(t, err.Error(), "i/o timeout", "Error should be a timeout")
		}

		// Allow small sleep for generic async cleanup
		time.Sleep(10 * time.Millisecond)
	}

	// Final FD count
	finalFDs := getFDCount()
	fmt.Printf("Final File Descriptors: %d\n", finalFDs)

	delta := finalFDs - initialFDs
	fmt.Printf("FD Delta: %d\n", delta)

	// 6. Verify stream count
	count := 0
	for _, conn := range h1.Network().Conns() {
		count += len(conn.GetStreams())
	}

	fmt.Printf("Active streams after test: %d\n", count)

	// Check for failures
	// If fix is missing, count should be ~50.
	if count > 5 {
		fmt.Println("FAILURE: Leaking streams detected!")
		t.Fail()
	} else {
		fmt.Println("SUCCESS: No leaks detected.")
	}
}
