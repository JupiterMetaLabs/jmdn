package bft

import (
	"crypto/sha256"
	"encoding/binary"
)

// DigestPrepare creates a canonical digest of the PrepareMessage for signing
// Fields: Version, Seq, Round, BlockHash, BuddyID, Decision, Timestamp
func DigestPrepare(msg *PrepareMessage) []byte {
	h := sha256.New()

	// Version
	h.Write([]byte(msg.Version))

	// Seq (uint64)
	binary.Write(h, binary.BigEndian, msg.Seq)

	// Round (uint64)
	binary.Write(h, binary.BigEndian, msg.Round)

	// BlockHash (string)
	h.Write([]byte(msg.BlockHash))

	// BuddyID (string)
	h.Write([]byte(msg.BuddyID))

	// Decision (string/enum)
	h.Write([]byte(msg.Decision))

	// Timestamp (int64)
	binary.Write(h, binary.BigEndian, msg.Timestamp)

	return h.Sum(nil)
}

// DigestCommit creates a canonical digest of the CommitMessage for signing
// Fields: Version, Seq, Round, BlockHash, BuddyID, Decision, Timestamp
// Note: PrepareProof is NOT included in the digest of the Commit itself,
// but each Prepare in the proof MUST have its own valid signature.
func DigestCommit(msg *CommitMessage) []byte {
	h := sha256.New()

	// Version
	h.Write([]byte(msg.Version))

	// Seq (uint64)
	binary.Write(h, binary.BigEndian, msg.Seq)

	// Round (uint64)
	binary.Write(h, binary.BigEndian, msg.Round)

	// BlockHash (string)
	h.Write([]byte(msg.BlockHash))

	// BuddyID (string)
	h.Write([]byte(msg.BuddyID))

	// Decision (string/enum)
	h.Write([]byte(msg.Decision))

	// Timestamp (int64)
	binary.Write(h, binary.BigEndian, msg.Timestamp)

	return h.Sum(nil)
}
