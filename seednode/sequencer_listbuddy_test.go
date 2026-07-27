package seednode

import (
	"context"
	"encoding/hex"
	"strconv"
	"testing"

	"gossipnode/seednode/committee"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/grpc/metadata"
)

// The sequencer-auth metadata attached to a ListBuddy call must carry a
// timestamp + a signature the seed accepts: signed by the sequencer identity key
// over jmdt/seed-auth/v1|ListBuddy|<peer_id>|<ts>.
func TestSequencerAuthContext_SignatureVerifies(t *testing.T) {
	priv, _, err := ic.GenerateKeyPair(ic.Ed25519, 0)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	ctx, err := sequencerAuthContext(context.Background(), priv)
	if err != nil {
		t.Fatalf("sequencerAuthContext: %v", err)
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
	// Reconstruct the exact challenge the seed will verify.
	ok, err = priv.GetPublic().Verify(committee.SequencerAuthChallenge(seqAuthMethod, pid.String(), ts), sig)
	if err != nil || !ok {
		t.Fatalf("attached signature must verify against the sequencer key: ok=%v err=%v", ok, err)
	}

	// Tamper: a different method must not verify against the same signature.
	if ok, _ := priv.GetPublic().Verify(committee.SequencerAuthChallenge("GetPeer", pid.String(), ts), sig); ok {
		t.Fatal("signature must not verify for a different method")
	}
}
