package bft

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignatureVerification(t *testing.T) {
	// 1. Setup Keys and Buddies
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	buddyID := "buddy-1"
	buddies := map[string]*BuddyInput{
		buddyID: {
			ID:        buddyID,
			PublicKey: pubKey,
		},
	}

	// 2. Setup Engine
	cfg := DefaultConfig()
	cfg.RequireSignatures = true // ENFORCE SIGNATURES

	e := &engine{
		config:      cfg,
		myBuddyID:   "self",
		buddies:     buddies,
		round:       1,
		blockHash:   "hash123",
		prepareMsgs: make(map[string]*PrepareMessage),
		commitMsgs:  make(map[string]*CommitMessage),
		lastSeqSeen: make(map[string]uint64),
	}

	t.Run("ValidatePrepare", func(t *testing.T) {
		// Test 1: Valid
		msg := &PrepareMessage{
			Version:   PrepareVersionV1,
			Seq:       1,
			Round:     1,
			BlockHash: "hash123",
			BuddyID:   buddyID,
			Decision:  Accept,
			Timestamp: time.Now().UTC().Unix(),
		}
		digest := DigestPrepare(msg)
		msg.Signature = ed25519.Sign(privKey, digest)

		err := e.validatePrepare(msg)
		assert.NoError(t, err, "Valid signature should pass")

		// Test 2: Invalid Signature (New Seq)
		badMsg := *msg
		badMsg.Seq = 2 // Increment Seq
		badMsg.Signature = make([]byte, len(msg.Signature))
		copy(badMsg.Signature, msg.Signature)
		badMsg.Signature[0] ^= 0xFF

		err = e.validatePrepare(&badMsg)
		assert.Error(t, err, "Invalid signature should fail")
		assert.Contains(t, err.Error(), "invalid signature")

		// Test 3: Missing Signature (New Seq)
		noSigMsg := *msg
		noSigMsg.Seq = 3 // Increment Seq
		noSigMsg.Signature = nil

		err = e.validatePrepare(&noSigMsg)
		assert.Error(t, err, "Missing signature should fail")
		assert.Contains(t, err.Error(), "missing signature")

		// Test 4: Tampered Payload (New Seq)
		tamperedMsg := *msg
		tamperedMsg.Seq = 4 // Increment Seq
		tamperedMsg.BlockHash = "hash999"

		err = e.validatePrepare(&tamperedMsg)
		assert.Error(t, err, "Tampered payload should fail verification")
	})

	t.Run("ValidateCommit", func(t *testing.T) {
		// Test 1: Valid
		msg := &CommitMessage{
			Version:   CommitVersionV1,
			Seq:       10, // Higher seq
			Round:     1,
			BlockHash: "hash123",
			BuddyID:   buddyID,
			Decision:  Accept,
			Timestamp: time.Now().UTC().Unix(),
		}

		// Proof MUST have valid signatures too
		proofMsg := PrepareMessage{
			Version:   PrepareVersionV1,
			Seq:       1, // Proof seq doesn't matter for Commit's seq check, but signature must be valid for THIS content
			Round:     1,
			BlockHash: "hash123",
			BuddyID:   buddyID,
			Decision:  Accept,
			Timestamp: time.Now().UTC().Unix(),
		}
		proofDigest := DigestPrepare(&proofMsg)
		proofMsg.Signature = ed25519.Sign(privKey, proofDigest)

		msg.PrepareProof = []PrepareMessage{proofMsg}

		commitDigest := DigestCommit(msg)
		msg.Signature = ed25519.Sign(privKey, commitDigest)

		err := e.validateCommit(msg)
		assert.NoError(t, err, "Valid commit and proof should pass")

		// Test 2: Invalid Commit Signature
		badCommit := *msg
		badCommit.Seq = 11 // Increment
		badCommit.Signature = make([]byte, len(msg.Signature))
		copy(badCommit.Signature, msg.Signature)
		badCommit.Signature[0] ^= 0xFF

		err = e.validateCommit(&badCommit)
		assert.Error(t, err, "Invalid commit signature should fail")
		assert.Contains(t, err.Error(), "invalid commit signature")

		// Test 3: Invalid Proof Signature
		// Reset proof signature to valid state first
		msg.PrepareProof[0].Signature = ed25519.Sign(privKey, DigestPrepare(&msg.PrepareProof[0]))

		badProofCommit := *msg
		badProofCommit.Seq = 12 // Increment
		// Re-sign OUTER commit because Seq changed
		badProofCommit.Signature = ed25519.Sign(privKey, DigestCommit(&badProofCommit))

		// Corrupt proof signature - we need to deep copy proof slice
		badProofCommit.PrepareProof = make([]PrepareMessage, 1)
		badProofCommit.PrepareProof[0] = msg.PrepareProof[0]
		badProofSignature := make([]byte, len(msg.PrepareProof[0].Signature))
		copy(badProofSignature, msg.PrepareProof[0].Signature)
		badProofSignature[0] ^= 0xFF
		badProofCommit.PrepareProof[0].Signature = badProofSignature

		err = e.validateCommit(&badProofCommit)
		assert.Error(t, err, "Invalid proof signature should fail")
		assert.Contains(t, err.Error(), "invalid signature in prepare proof")
	})
}
