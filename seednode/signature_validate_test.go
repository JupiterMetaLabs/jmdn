package seednode

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"

	peerpb "gossipnode/seednode/proto"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func ed25519Identity(t *testing.T) (crypto.PrivKey, peer.ID) {
	t.Helper()
	// Production nodes use Ed25519 (node/node.go); those peer IDs embed the
	// public key so ExtractPublicKey works for Verify without a peerstore.
	priv, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	return priv, pid
}

func signCanonical(t *testing.T, priv crypto.PrivKey, message string) (r, s, v string) {
	t.Helper()
	hash := sha256.Sum256([]byte(message))
	sig, err := priv.Sign(hash[:])
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig) < 64 {
		t.Fatalf("unexpected sig len %d", len(sig))
	}
	rb := new(big.Int).SetBytes(sig[:32])
	sb := new(big.Int).SetBytes(sig[32:64])
	vv := calculateVFromSignature(rb, sb, hash[:])
	return hex.EncodeToString(rb.Bytes()), hex.EncodeToString(sb.Bytes()), hex.EncodeToString([]byte{vv})
}

func TestValidatePeerRecordSignature_RejectsUnsignedAndTampered(t *testing.T) {
	priv, pid := ed25519Identity(t)
	rec := &peerpb.SignedPeerRecord{
		PeerId:        pid.String(),
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/15000"},
		Seq:           7,
		CurrentStatus: peerpb.PeerStatus_PEER_STATUS_ACTIVE,
	}

	if err := ValidatePeerRecordSignature(rec); err == nil {
		t.Fatal("unsigned peer record must be rejected")
	}

	r, s, v := signCanonical(t, priv, peerRecordCanonicalMessage(rec))
	rec.R, rec.S, rec.V = r, s, v
	if err := ValidatePeerRecordSignature(rec); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}

	// Tamper multiaddr after signing — signature must no longer verify.
	rec.Multiaddrs[0] = "/ip4/9.9.9.9/tcp/15000"
	if err := ValidatePeerRecordSignature(rec); err == nil {
		t.Fatal("tampered peer record must be rejected")
	}
}

func TestValidateHeartbeatSignature_RejectsUnsignedAndTampered(t *testing.T) {
	priv, pid := ed25519Identity(t)
	hb := &peerpb.HeartbeatMessage{
		PeerId:     pid.String(),
		Status:     peerpb.PeerStatus_PEER_STATUS_ACTIVE,
		Multiaddrs: []string{"/ip4/1.2.3.4/tcp/15000"},
	}
	if err := ValidateHeartbeatSignature(hb); err == nil {
		t.Fatal("unsigned heartbeat must be rejected")
	}
	var parts []string
	parts = append(parts, hb.PeerId, hb.Status.String())
	parts = append(parts, hb.Multiaddrs...)
	joined := ""
	for i, p := range parts {
		if i > 0 {
			joined += "|"
		}
		joined += p
	}
	r, s, v := signCanonical(t, priv, joined)
	hb.R, hb.S, hb.V = r, s, v
	if err := ValidateHeartbeatSignature(hb); err != nil {
		t.Fatalf("valid heartbeat rejected: %v", err)
	}
	hb.Status = peerpb.PeerStatus_PEER_STATUS_INACTIVE
	if err := ValidateHeartbeatSignature(hb); err == nil {
		t.Fatal("tampered heartbeat must be rejected")
	}
}

func TestValidateAliasSignature_RejectsUnsignedAndTampered(t *testing.T) {
	priv, pid := ed25519Identity(t)
	alias := &peerpb.PeerAlias{Name: "alice", PeerId: pid.String()}
	if err := ValidateAliasSignature(alias); err == nil {
		t.Fatal("unsigned alias must be rejected")
	}
	r, s, v := signCanonical(t, priv, alias.Name+"|"+alias.PeerId)
	alias.R, alias.S, alias.V = r, s, v
	if err := ValidateAliasSignature(alias); err != nil {
		t.Fatalf("valid alias rejected: %v", err)
	}
	alias.Name = "eve"
	if err := ValidateAliasSignature(alias); err == nil {
		t.Fatal("tampered alias must be rejected")
	}
}

func TestValidateNeighborSignature_RejectsUnsignedAndTampered(t *testing.T) {
	priv, pid := ed25519Identity(t)
	n := &peerpb.PeerNeighbor{
		PeerId:     pid.String(),
		NeighborId: "neighbor-peer",
		CreatedAt:  100,
		LastSeen:   200,
		IsActive:   true,
	}
	if err := ValidateNeighborSignature(n); err == nil {
		t.Fatal("unsigned neighbor must be rejected")
	}
	msg := fmt.Sprintf("%s|%s|%d|%d|%t", n.PeerId, n.NeighborId, n.CreatedAt, n.LastSeen, n.IsActive)
	r, s, v := signCanonical(t, priv, msg)
	n.R, n.S, n.V = r, s, v
	if err := ValidateNeighborSignature(n); err != nil {
		t.Fatalf("valid neighbor rejected: %v", err)
	}
	n.IsActive = false
	if err := ValidateNeighborSignature(n); err == nil {
		t.Fatal("tampered neighbor must be rejected")
	}
}

func TestFilterValidPeerRecords_DropsBad(t *testing.T) {
	priv, pid := ed25519Identity(t)
	good := &peerpb.SignedPeerRecord{
		PeerId:        pid.String(),
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/1"},
		Seq:           1,
		CurrentStatus: peerpb.PeerStatus_PEER_STATUS_ACTIVE,
	}
	r, s, v := signCanonical(t, priv, peerRecordCanonicalMessage(good))
	good.R, good.S, good.V = r, s, v
	bad := &peerpb.SignedPeerRecord{
		PeerId:        pid.String(),
		Multiaddrs:    []string{"/ip4/9.9.9.9/tcp/1"},
		Seq:           2,
		CurrentStatus: peerpb.PeerStatus_PEER_STATUS_ACTIVE,
		// unsigned
	}
	out := filterValidPeerRecords([]*peerpb.SignedPeerRecord{good, bad, nil})
	if len(out) != 1 || out[0] != good {
		t.Fatalf("expected only good record, got %d", len(out))
	}
}
