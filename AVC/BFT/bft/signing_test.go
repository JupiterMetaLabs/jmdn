package bft

// The BFT engine must sign its outgoing PREPARE/COMMIT messages so that,
// under the secure-default RequireSignatures, peers accept them — and reject
// unsigned or tampered ones.

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func mkPrepare(seq uint64, d Decision) *PrepareMessage {
	return &PrepareMessage{
		Version:   PrepareVersionV1,
		Seq:       seq,
		Round:     1,
		BlockHash: "0xabc",
		BuddyID:   "b1",
		Decision:  d,
		Timestamp: time.Now().UTC().Unix(),
	}
}

// TestEngineSignsVerifiably proves signPrepare/signCommit produce signatures
// that verify against the corresponding ed25519 public key over the SAME digest
// peers use.
func TestEngineSignsVerifiably(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	e := &engine{config: DefaultConfig(), signer: NewLocalSigner(priv)}

	p := mkPrepare(1, Accept)
	if err := e.signPrepare(p); err != nil {
		t.Fatalf("signPrepare: %v", err)
	}
	if len(p.Signature) == 0 {
		t.Fatal("prepare was not signed")
	}
	if !ed25519.Verify(pub, DigestPrepare(p), p.Signature) {
		t.Fatal("engine PREPARE signature does not verify against the public key")
	}

	c := &CommitMessage{
		Version: CommitVersionV1, Seq: 1, Round: 1, BlockHash: "0xabc",
		BuddyID: "self", Decision: Accept, Timestamp: time.Now().UTC().Unix(),
	}
	if err := e.signCommit(c); err != nil {
		t.Fatalf("signCommit: %v", err)
	}
	if !ed25519.Verify(pub, DigestCommit(c), c.Signature) {
		t.Fatal("engine COMMIT signature does not verify against the public key")
	}
}

// TestValidatePrepareAcceptsSignedRejectsRest exercises the verify path with
// RequireSignatures on.
func TestValidatePrepareAcceptsSignedRejectsRest(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	e := &engine{
		config:      DefaultConfig(), // RequireSignatures = true
		myBuddyID:   "self",
		round:       1,
		blockHash:   "0xabc",
		buddies:     map[string]*BuddyInput{"b1": {ID: "b1", PublicKey: pub}},
		prepareMsgs: make(map[string]*PrepareMessage),
		lastSeqSeen: make(map[string]uint64),
	}
	signer := NewLocalSigner(priv)

	// Correctly signed → accepted.
	p := mkPrepare(1, Accept)
	sig, err := signer.Sign(DigestPrepare(p))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	p.Signature = sig
	if err := e.validatePrepare(p); err != nil {
		t.Fatalf("signed prepare should validate: %v", err)
	}

	// Unsigned → rejected.
	if err := e.validatePrepare(mkPrepare(2, Accept)); err == nil {
		t.Fatal("unsigned prepare must be rejected under RequireSignatures")
	}

	// Signed then tampered (decision flipped changes the digest) → rejected.
	tp := mkPrepare(3, Accept)
	s3, _ := signer.Sign(DigestPrepare(tp))
	tp.Signature = s3
	tp.Decision = Reject
	if err := e.validatePrepare(tp); err == nil {
		t.Fatal("tampered prepare must be rejected")
	}

	// Signed by the WRONG key → rejected.
	_, wrongPriv, _ := ed25519.GenerateKey(rand.Reader)
	wp := mkPrepare(4, Accept)
	ws, _ := NewLocalSigner(wrongPriv).Sign(DigestPrepare(wp))
	wp.Signature = ws
	if err := e.validatePrepare(wp); err == nil {
		t.Fatal("prepare signed by a non-committee key must be rejected")
	}
}

// TestNoSignerFailsClosed: with RequireSignatures set and no signer, the
// engine refuses to emit an (unsigned) message rather than one peers will drop.
func TestNoSignerFailsClosed(t *testing.T) {
	e := &engine{config: DefaultConfig()} // RequireSignatures true, signer nil
	if err := e.signPrepare(mkPrepare(1, Accept)); err == nil {
		t.Fatal("RequireSignatures with no signer must error")
	}
	if err := e.signCommit(&CommitMessage{Version: CommitVersionV1}); err == nil {
		t.Fatal("RequireSignatures with no signer must error for commit")
	}
}
