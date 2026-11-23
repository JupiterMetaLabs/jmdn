package BLS_Verifier

import (
	"encoding/hex"
	"fmt"
	"strconv"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// messageForVote returns the canonical message bytes used for signing a vote
func messageForVote(vote int8) ([]byte, error) {
	if vote != -1 && vote != 1 {
		return nil, fmt.Errorf("invalid vote: %d", vote)
	}
	return []byte("vote:" + strconv.Itoa(int(vote))), nil
}

// VerifyForBlock verifies a response's signature against the block-bound vote
// message: chain id + HEIGHT + block hash + ConsensusHash + vote. A signature
// that passes is an attestation for THIS block, on THIS chain, at THIS height,
// committing to THESE consensus fields.
//
// There is exactly one accepted format (BLS_Signer.CanonicalVoteMessageV3,
// which requires a non-empty consensusHash), so there is no downgrade path to
// disable. An empty consensusHash is rejected here, by
// CanonicalVoteMessageV3's own validation, rather than silently falling back
// to a weaker format that doesn't bind the consensus fields — that fallback
// was the vulnerability: a verifier willing to accept a no-ConsensusHash signature
// could never catch a forged ConsensusHash, no matter how the hash-bearing
// format was built.
func VerifyForBlock(resp BLS_Signer.BLSresponse, chainID, height uint64, bindings, consensusHash string, vote int8) error {
	msg, err := BLS_Signer.CanonicalVoteMessageV3(chainID, height, bindings, consensusHash, vote)
	if err != nil {
		return fmt.Errorf("build block-bound vote message: %w", err)
	}
	if err := verifyCanonical(resp, msg); err != nil {
		return fmt.Errorf("bls verify (block-bound) failed for peer %s: %w", resp.PeerID, err)
	}
	return nil
}

// VerifyBallotForBlock checks a gossiped ballot. It accepts only the ballot
// domain (BLS_Signer.CanonicalBallotMessageV1).
//
// The vote-result format (CanonicalVoteMessageV3) is not accepted here. A
// member signs its vote result in that format, over the committee's
// aggregated outcome, which can differ from its own ballot; accepting it as a
// ballot would let anyone holding that vote result present it as a second,
// conflicting ballot from the member (false equivocation evidence).
// Certificate checks use VerifyForBlock, which rejects the ballot domain, so
// the two formats never verify as each other.
//
// Rollout: this release is deployed to every node together (a coordinated
// fleet-wide upgrade). A node on the previous release signs ballots in the
// vote-result format, which this release does not count, so ballots between
// old and new nodes are dropped until every node has restarted; a rolling
// upgrade is not supported.
func VerifyBallotForBlock(resp BLS_Signer.BLSresponse, chainID, height uint64, bindings, consensusHash string, vote int8) error {
	msg, err := BLS_Signer.CanonicalBallotMessageV1(chainID, height, bindings, consensusHash, vote)
	if err != nil {
		return fmt.Errorf("build ballot message: %w", err)
	}
	if err := verifyCanonical(resp, msg); err != nil {
		return fmt.Errorf("bls verify (ballot) failed for peer %s: %w", resp.PeerID, err)
	}
	return nil
}

// verifyCanonical checks resp's signature over msg with resp's public key.
func verifyCanonical(resp BLS_Signer.BLSresponse, msg []byte) error {
	pubBytes, err := hex.DecodeString(resp.PubKey)
	if err != nil {
		return fmt.Errorf("invalid pubkey hex: %w", err)
	}
	sigBytes, err := hex.DecodeString(resp.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}
	return blssign.BLSVerify(pubBytes, msg, sigBytes)
}

// Verify checks a single BLS response against the provided vote value.
// Returns nil if signature is valid for that vote; error otherwise.
func Verify(resp BLS_Signer.BLSresponse, vote int8) error {
	msg, err := messageForVote(vote)
	if err != nil {
		return err
	}

	// Decode hex-encoded pubkey and signature
	pubBytes, err := hex.DecodeString(resp.PubKey)
	if err != nil {
		return fmt.Errorf("invalid pubkey hex: %w", err)
	}
	sigBytes, err := hex.DecodeString(resp.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}

	if err := blssign.BLSVerify(pubBytes, msg, sigBytes); err != nil {
		return fmt.Errorf("bls verify failed for peer %s: %w", resp.PeerID, err)
	}
	return nil
}
