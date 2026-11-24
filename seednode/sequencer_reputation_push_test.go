package seednode

// A4-COMPLETION-LLD.md §6 tests for Phase A4.2's push RPC.

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"

	"gossipnode/seednode/committee"
	peerpb "gossipnode/seednode/proto"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakePeerDirectoryClient embeds the (nil) interface so only the one method
// under test needs a real implementation — calling any other method would
// panic on the nil embedded value, which is the correct failure mode for a
// test that should never reach it.
type fakePeerDirectoryClient struct {
	peerpb.PeerDirectoryClient
	updateFn func(ctx context.Context, in *peerpb.UpdatePeerWeightsRequest) (*peerpb.UpdatePeerWeightsResponse, error)
}

func (f *fakePeerDirectoryClient) UpdatePeerWeights(ctx context.Context, in *peerpb.UpdatePeerWeightsRequest, _ ...grpc.CallOption) (*peerpb.UpdatePeerWeightsResponse, error) {
	return f.updateFn(ctx, in)
}

func TestPushReputationWeights_NoSequencerKeyIsErrNotSequencerNoRPC(t *testing.T) {
	old := currentSequencerSignKey()
	SetSequencerSignKey(nil)
	defer SetSequencerSignKey(old)

	called := false
	c := &Client{client: &fakePeerDirectoryClient{updateFn: func(context.Context, *peerpb.UpdatePeerWeightsRequest) (*peerpb.UpdatePeerWeightsResponse, error) {
		called = true
		return &peerpb.UpdatePeerWeightsResponse{}, nil
	}}}

	accepted, failures := c.PushReputationWeights(context.Background(), map[string]float64{"peerA": 0.7})
	if accepted != 0 {
		t.Errorf("accepted = %d, want 0", accepted)
	}
	if len(failures) != 1 || !errors.Is(failures[0], ErrNotSequencer) {
		t.Fatalf("failures = %v, want exactly [ErrNotSequencer]", failures)
	}
	if called {
		t.Error("no RPC should be attempted when this node has no registered sequencer key")
	}
}

func TestPushReputationWeights_EmptyWeightsIsNoOp(t *testing.T) {
	priv, _, err := ic.GenerateKeyPair(ic.Ed25519, 0)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	old := currentSequencerSignKey()
	SetSequencerSignKey(priv)
	defer SetSequencerSignKey(old)

	called := false
	c := &Client{client: &fakePeerDirectoryClient{updateFn: func(context.Context, *peerpb.UpdatePeerWeightsRequest) (*peerpb.UpdatePeerWeightsResponse, error) {
		called = true
		return &peerpb.UpdatePeerWeightsResponse{}, nil
	}}}

	accepted, failures := c.PushReputationWeights(context.Background(), map[string]float64{})
	if accepted != 0 || failures != nil {
		t.Errorf("got accepted=%d failures=%v, want 0, nil", accepted, failures)
	}
	if called {
		t.Error("no RPC should be attempted for an empty weights map")
	}
}

func TestPushReputationWeights_PartialBatchFailureDoesNotAbort(t *testing.T) {
	priv, _, err := ic.GenerateKeyPair(ic.Ed25519, 0)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	old := currentSequencerSignKey()
	SetSequencerSignKey(priv)
	defer SetSequencerSignKey(old)

	c := &Client{client: &fakePeerDirectoryClient{updateFn: func(_ context.Context, in *peerpb.UpdatePeerWeightsRequest) (*peerpb.UpdatePeerWeightsResponse, error) {
		if in.PeerId == "bad" {
			return nil, errors.New("boom")
		}
		return &peerpb.UpdatePeerWeightsResponse{}, nil
	}}}

	accepted, failures := c.PushReputationWeights(context.Background(), map[string]float64{
		"good1": 0.7, "bad": 0.3, "good2": 0.8,
	})
	if accepted != 2 {
		t.Errorf("accepted = %d, want 2 (the two good peers)", accepted)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly 1 (the bad peer)", failures)
	}
}

// V/R/S are deliberately left empty (see the PHASE A4.2 CAVEAT comment) —
// confirm the request actually reflects that rather than accidentally
// carrying stray data.
func TestPushReputationWeights_LeavesVRSEmpty(t *testing.T) {
	priv, _, err := ic.GenerateKeyPair(ic.Ed25519, 0)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	old := currentSequencerSignKey()
	SetSequencerSignKey(priv)
	defer SetSequencerSignKey(old)

	var got *peerpb.UpdatePeerWeightsRequest
	c := &Client{client: &fakePeerDirectoryClient{updateFn: func(_ context.Context, in *peerpb.UpdatePeerWeightsRequest) (*peerpb.UpdatePeerWeightsResponse, error) {
		got = in
		return &peerpb.UpdatePeerWeightsResponse{}, nil
	}}}

	if _, failures := c.PushReputationWeights(context.Background(), map[string]float64{"peerA": 0.42}); len(failures) != 0 {
		t.Fatalf("unexpected failures: %v", failures)
	}
	if got == nil {
		t.Fatal("RPC was never called")
	}
	if got.V != "" || got.R != "" || got.S != "" {
		t.Errorf("V/R/S must stay empty, got V=%q R=%q S=%q", got.V, got.R, got.S)
	}
	if got.PeerId != "peerA" || got.Weights != float32(0.42) {
		t.Errorf("got PeerId=%q Weights=%v, want peerA/0.42", got.PeerId, got.Weights)
	}
}

// The attached auth headers must carry a signature that actually verifies
// against SequencerAuthChallenge for this RPC's own method string — the one
// genuinely new crypto path in this file, mirroring
// TestSequencerAuthContext_SignatureVerifies's shape for ListBuddy.
func TestSequencerAuthContextForMethod_SignatureVerifies(t *testing.T) {
	priv, _, err := ic.GenerateKeyPair(ic.Ed25519, 0)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	ctx, err := sequencerAuthContextForMethod(context.Background(), priv, reputationPushAuthMethod)
	if err != nil {
		t.Fatalf("sequencerAuthContextForMethod: %v", err)
	}
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("no outgoing metadata attached")
	}
	tsVals := md.Get(committee.SeqAuthTimestampHeader)
	sigVals := md.Get(committee.SeqAuthSignatureHeader)
	if len(tsVals) != 1 || len(sigVals) != 1 {
		t.Fatalf("expected one ts + one sig header, got ts=%v sig=%v", tsVals, sigVals)
	}

	ts, err := strconv.ParseInt(tsVals[0], 10, 64)
	if err != nil {
		t.Fatalf("bad ts %q: %v", tsVals[0], err)
	}
	sig, err := hex.DecodeString(sigVals[0])
	if err != nil {
		t.Fatalf("bad sig hex: %v", err)
	}
	pid, _ := peer.IDFromPublicKey(priv.GetPublic())

	ok2, err := priv.GetPublic().Verify(committee.SequencerAuthChallenge(reputationPushAuthMethod, pid.String(), ts), sig)
	if err != nil || !ok2 {
		t.Fatalf("attached signature must verify against the sequencer key: ok=%v err=%v", ok2, err)
	}

	// Must NOT verify against the ListBuddy method string — confirms this
	// call is bound to its own method, not accidentally reusing ListBuddy's
	// signed challenge (which would let a captured ListBuddy auth header be
	// replayed here, or vice versa).
	if ok3, _ := priv.GetPublic().Verify(committee.SequencerAuthChallenge(seqAuthMethod, pid.String(), ts), sig); ok3 {
		t.Fatal("signature must not verify for a different method (cross-method replay)")
	}
}
