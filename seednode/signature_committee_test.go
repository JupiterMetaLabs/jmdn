package seednode

import (
	"testing"

	peerpb "gossipnode/seednode/proto"
)

// The identity-signed peer-record message must match seedNodes
// ValidatePeerRecordSignature (pkg/peer/vrsSigner.go) byte-for-byte:
//
//	peer_id | multiaddrs… | seq | status [ | bls_pub(lowercase) ]
//
// with bls_pub appended ONLY when set.
func TestPeerRecordCanonicalMessage_BLSAppend(t *testing.T) {
	base := &peerpb.SignedPeerRecord{
		PeerId:        "QmPeer",
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/15000"},
		Seq:           7,
		CurrentStatus: peerpb.PeerStatus(0),
	}
	statusStr := base.CurrentStatus.String()

	// No bls_pub -> base payload (backward-compatible, no trailing pipe).
	got := peerRecordCanonicalMessage(base)
	want := "QmPeer|/ip4/1.2.3.4/tcp/15000|7|" + statusStr
	if got != want {
		t.Fatalf("base payload=%q want %q", got, want)
	}

	// bls_pub set (mixed case) -> appended lowercase.
	withBLS := &peerpb.SignedPeerRecord{
		PeerId:        "QmPeer",
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/15000"},
		Seq:           7,
		CurrentStatus: peerpb.PeerStatus(0),
		BlsPub:        "AABBcc",
	}
	got2 := peerRecordCanonicalMessage(withBLS)
	want2 := "QmPeer|/ip4/1.2.3.4/tcp/15000|7|" + statusStr + "|aabbcc"
	if got2 != want2 {
		t.Fatalf("committee payload=%q want %q", got2, want2)
	}
}
