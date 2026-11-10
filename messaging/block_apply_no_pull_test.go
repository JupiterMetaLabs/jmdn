package messaging

// F1: before applying a received block, the node pulled contract code from
// every peer for each Type-2 recipient (any EIP-1559 transaction, including a
// plain transfer), and the read had no deadline. A connected peer that accepted
// the pull stream and never answered froze block apply. The pull never served
// execution (it wrote sharedKVStore; IsContractTx reads cas.KV()), so the pull
// client is gone and neither the v3 nor the legacy-follow apply path pulls.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"

	"gossipnode/config"
	"gossipnode/config/GRO"
	GROHelper "gossipnode/messaging/common"
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

// The legacy-follow apply path (#276) pulled the same way before applying a
// legacy block from the main sequencer. It must not touch the network either.
func TestHandleLegacyBlockMessage_OpensNoContractPull(t *testing.T) {
	resetEquivocation()
	t.Cleanup(resetEquivocation)
	if BlockPropagationLocalGRO == nil { // started at boot on a node
		g, err := GROHelper.InitializeGRO(GRO.BlockPropagationLocal)
		if err != nil {
			t.Fatal(err)
		}
		BlockPropagationLocalGRO = g
	}
	main := testPeerID(t)
	withLegacyFollow(t, true, main, 0)
	victim := newLoopbackHost(t)
	prevHost := getHostInstance()
	SetHostInstance(victim)
	t.Cleanup(func() { SetHostInstance(prevHost) })
	opened, _ := silentPullPeer(t, victim)

	key, _ := crypto.GenerateKey()
	b := mainBlock(t, 60, signedTx(t, key, 0)) // an ordinary EIP-1559 transfer
	if !IsLegacyBlock(b) {
		t.Fatal("test premise: the block is not handled by the legacy rules")
	}
	HandleReceivedBlockMessage(config.BlockMessage{Type: "zkblock", Data: map[string]string{}, Block: b}, main.String(), true)
	if !isMessageProcessed(legacyBlockDedupID(b)) {
		t.Fatal("test premise: the legacy gate did not admit the block, so its apply goroutine never ran")
	}
	time.Sleep(500 * time.Millisecond) // let the apply goroutine reach the network if it would
	if n := atomic.LoadInt32(opened); n != 0 {
		t.Fatalf("the legacy apply path opened %d contract-pull stream(s); it must not touch the network", n)
	}
}

// The pull server (still registered for older nodes that pull) gives a peer
// that opens a stream and never finishes its request at most pullStreamTimeout.
func TestHandleContractPullStream_IdleRequestIsBounded(t *testing.T) {
	prev := pullStreamTimeout
	pullStreamTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pullStreamTimeout = prev })

	server, client := newLoopbackHost(t), newLoopbackHost(t)
	returned := make(chan struct{})
	server.SetStreamHandler(config.ContractPullProtocol, func(s network.Stream) {
		HandleContractPullStream(s)
		close(returned)
	})
	connect(t, client, server)

	s, err := client.NewStream(context.Background(), server.ID(), config.ContractPullProtocol)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Reset() })
	// Start a request and never finish it (no newline, stream left open).
	if _, err := s.Write([]byte(`{"contract_address":`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("the pull server is still waiting on a request that never finishes")
	}
}
