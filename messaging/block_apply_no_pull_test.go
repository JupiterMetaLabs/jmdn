package messaging

// F1: before applying a received block, the node pulled contract code from
// every peer for each Type-2 recipient (any EIP-1559 transaction, including a
// plain transfer), and the read had no deadline. A connected peer that accepted
// the pull stream and never answered froze block apply. The pull never served
// execution (it wrote sharedKVStore; IsContractTx reads cas.KV()), so it is no
// longer on the apply path.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"

	"gossipnode/config"
)

// silentPullPeer connects a peer that accepts contract-pull streams, counts
// them, and never answers. The returned func releases its handlers.
func silentPullPeer(t *testing.T, victim host.Host) (*int32, func()) {
	t.Helper()
	attacker := newLoopbackHost(t)
	var opened int32
	release := make(chan struct{})
	attacker.SetStreamHandler(config.ContractPullProtocol, func(s network.Stream) {
		atomic.AddInt32(&opened, 1)
		<-release
		_ = s.Reset()
	})
	connect(t, victim, attacker)
	var once atomic.Bool
	stop := func() {
		if once.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(stop)
	return &opened, stop
}

// A block with an ordinary EIP-1559 transfer: prepareReceivedBlock, the step
// between the gate and apply+store, returns at once and opens no pull stream,
// even with a silent pull peer connected.
func TestPrepareReceivedBlock_OpensNoContractPull(t *testing.T) {
	victim := newLoopbackHost(t)
	prevHost := getHostInstance()
	SetHostInstance(victim)
	t.Cleanup(func() { SetHostInstance(prevHost) })
	opened, _ := silentPullPeer(t, victim)

	to := common.HexToAddress("0x00000000000000000000000000000000000000b0")
	msg := config.BlockMessage{
		Type: "zkblock",
		Data: map[string]string{},
		Block: &config.ZKBlock{
			BlockNumber:  7,
			BlockHash:    common.HexToHash("0xf1"),
			Transactions: []config.Transaction{{Type: 2, To: &to}},
		},
	}
	done := make(chan struct{})
	go func() { prepareReceivedBlock(msg); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("prepareReceivedBlock blocked with a silent peer connected: block apply would freeze")
	}
	time.Sleep(200 * time.Millisecond) // let any stray stream reach the peer
	if n := atomic.LoadInt32(opened); n != 0 {
		t.Fatalf("the apply path opened %d contract-pull stream(s); it must not touch the network", n)
	}
}

// The pull client itself, if anything calls it again, gives a silent peer at
// most pullStreamTimeout instead of forever.
func TestPullContractIfMissing_SilentPeerIsBounded(t *testing.T) {
	prev := pullStreamTimeout
	pullStreamTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pullStreamTimeout = prev })

	victim := newLoopbackHost(t)
	opened, _ := silentPullPeer(t, victim)

	done := make(chan bool, 1)
	start := time.Now()
	go func() {
		done <- PullContractIfMissing(context.Background(), victim, common.HexToAddress("0x00000000000000000000000000000000000000b1"))
	}()
	select {
	case found := <-done:
		if found {
			t.Fatal("a silent peer cannot have supplied the contract")
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("pull took %v with a %v per-peer deadline", took, pullStreamTimeout)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PullContractIfMissing is still waiting on a silent peer")
	}
	if atomic.LoadInt32(opened) == 0 {
		t.Fatal("test premise: the silent peer was never asked")
	}
}
