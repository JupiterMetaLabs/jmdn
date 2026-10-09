package l1auth

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// newPeerID generates a valid libp2p peer ID (Ed25519) for tests.
func newPeerID(t *testing.T) string {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("IDFromPrivateKey: %v", err)
	}
	return id.String()
}

func TestEnforced(t *testing.T) {
	if Enforced("") {
		t.Fatal("empty trusted sequencer id must NOT be enforced (legacy/warn mode)")
	}
	if Enforced("   ") {
		t.Fatal("whitespace trusted id must NOT be enforced")
	}
	if !Enforced(newPeerID(t)) {
		t.Fatal("a configured trusted sequencer id MUST enforce")
	}
}

func TestIsSequencer(t *testing.T) {
	seq := newPeerID(t)
	attacker := newPeerID(t)

	if !IsSequencer(seq, seq) {
		t.Fatal("the sequencer's own authenticated id must be accepted")
	}
	if IsSequencer(attacker, seq) {
		t.Fatal("an attacker's authenticated id must be REJECTED (this is the spoof)")
	}
	// Fail-closed on empty / garbage inputs.
	for _, bad := range []struct{ sender, trusted string }{
		{"", seq},
		{seq, ""},
		{"", ""},
		{"not-a-peer-id", seq},
		{seq, "not-a-peer-id"},
	} {
		if IsSequencer(bad.sender, bad.trusted) {
			t.Fatalf("IsSequencer(%q, %q) must be false (fail-closed)", bad.sender, bad.trusted)
		}
	}
}

func TestValidatePin(t *testing.T) {
	if err := ValidatePin(""); err != nil {
		t.Fatalf("empty pin must be allowed (enforcement off), got %v", err)
	}
	if err := ValidatePin("  \t"); err != nil {
		t.Fatalf("whitespace pin must be treated as empty, got %v", err)
	}
	good := newPeerID(t)
	if err := ValidatePin(good); err != nil {
		t.Fatalf("valid peer id rejected: %v", err)
	}
	if err := ValidatePin(" " + good + " "); err != nil {
		t.Fatalf("valid peer id with surrounding whitespace rejected: %v", err)
	}
	// One-character corruption of a real ID must be rejected at startup rather
	// than silently dropping every genuine commit at runtime.
	typo := good[:len(good)-1] + "0"
	if typo == good {
		typo = good[:len(good)-1] + "1"
	}
	if err := ValidatePin(typo); err == nil {
		t.Fatalf("malformed pin %q must be rejected", typo)
	}
	if err := ValidatePin("not-a-peer-id"); err == nil {
		t.Fatal("garbage pin must be rejected")
	}
}
