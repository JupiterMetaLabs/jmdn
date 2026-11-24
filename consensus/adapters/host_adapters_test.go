package adapters_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/interfaces"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/consensus/adapters"
)

func TestPubSubAdapter_DelegatesAndRejectsNil(t *testing.T) {
	if _, err := adapters.NewPubSubAdapter(nil); err == nil {
		t.Fatal("nil publish must be rejected")
	}
	var gotTopic string
	var gotPayload []byte
	a, err := adapters.NewPubSubAdapter(func(_ context.Context, topic string, payload []byte) error {
		gotTopic, gotPayload = topic, payload
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Publish(context.Background(), "votes", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if gotTopic != "votes" || string(gotPayload) != "x" {
		t.Errorf("publish not delegated: topic=%q payload=%q", gotTopic, gotPayload)
	}
}

func TestNodeConfigAdapter_ReturnsIdentity(t *testing.T) {
	pid := peer.ID("12D3KooWSelfNodeAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	a := adapters.NewNodeConfigAdapter(pid, nil, nil)
	if a.PeerID() != pid {
		t.Errorf("PeerID = %q, want %q", a.PeerID(), pid)
	}
	if a.ListenAddresses() != nil {
		t.Errorf("ListenAddresses should be nil when none supplied")
	}
}

func TestPeerListerAdapter_DelegatesAndRejectsNil(t *testing.T) {
	if _, err := adapters.NewPeerListerAdapter(nil, nil); err == nil {
		t.Fatal("nil funcs must be rejected")
	}
	want := []peer.ID{peer.ID("a"), peer.ID("b")}
	a, err := adapters.NewPeerListerAdapter(
		func(context.Context) ([]peer.ID, error) { return want, nil },
		func(_ context.Context, id peer.ID) (interfaces.Node, error) { return interfaces.Node{ID: id}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.ListPeers(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("ListPeers = %v, %v", got, err)
	}
	n, _ := a.GetPeer(context.Background(), peer.ID("z"))
	if n.ID != peer.ID("z") {
		t.Errorf("GetPeer did not delegate: %v", n.ID)
	}
}

func TestVoteSinkAdapter_DelegatesAndRejectsNil(t *testing.T) {
	if _, err := adapters.NewVoteSinkAdapter(nil); err == nil {
		t.Fatal("nil store must be rejected")
	}
	var got interfaces.VoteResult
	a, _ := adapters.NewVoteSinkAdapter(func(r interfaces.VoteResult) error { got = r; return nil })
	if err := a.StoreVoteResult(interfaces.VoteResult{BlockHash: "0xabc", Accepted: true}); err != nil {
		t.Fatal(err)
	}
	if got.BlockHash != "0xabc" || !got.Accepted {
		t.Errorf("sink did not delegate: %+v", got)
	}
}

// fakeValidator is a stand-in for a FullValidator built per block.
type fakeValidator struct {
	verdict interfaces.Verdict
	err     error
}

func (f fakeValidator) ValidateBlock(interfaces.ZKBlock, interfaces.ValidationDepth) (interfaces.Verdict, error) {
	return f.verdict, f.err
}

func TestPerBlockValidator_DelegatesToBuiltValidator(t *testing.T) {
	built := 0
	v, err := adapters.NewPerBlockValidator(func(interfaces.ZKBlock) (interfaces.BlockValidator, error) {
		built++
		return fakeValidator{verdict: interfaces.Approved()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	vd, err := v.ValidateBlock(nil, interfaces.DepthFull)
	if err != nil || !vd.Accept {
		t.Fatalf("expected approve, got verdict=%+v err=%v", vd, err)
	}
	if built != 1 {
		t.Errorf("buildForBlock should be called once per ValidateBlock, got %d", built)
	}
}

// FAIL-CLOSED: a build error must veto (non-accept verdict AND non-nil error),
// so the engine's validateBeforeVote gate refuses to vote.
func TestPerBlockValidator_BuildErrorFailsClosed(t *testing.T) {
	v, _ := adapters.NewPerBlockValidator(func(interfaces.ZKBlock) (interfaces.BlockValidator, error) {
		return nil, errors.New("cache load failed")
	})
	vd, err := v.ValidateBlock(nil, interfaces.DepthFull)
	if err == nil {
		t.Fatal("build error must return a non-nil error (fail-closed veto)")
	}
	if vd.Accept {
		t.Fatal("build error must NOT accept the block")
	}
}

func TestPerBlockValidator_NilBuiltValidatorFailsClosed(t *testing.T) {
	v, _ := adapters.NewPerBlockValidator(func(interfaces.ZKBlock) (interfaces.BlockValidator, error) {
		return nil, nil // no error, but nil validator
	})
	vd, err := v.ValidateBlock(nil, interfaces.DepthFull)
	if err == nil || vd.Accept {
		t.Fatalf("nil built validator must fail closed, got verdict=%+v err=%v", vd, err)
	}
}

func TestNewPerBlockValidator_RejectsNilBuilder(t *testing.T) {
	if _, err := adapters.NewPerBlockValidator(nil); err == nil {
		t.Fatal("nil buildForBlock must be rejected")
	}
}
