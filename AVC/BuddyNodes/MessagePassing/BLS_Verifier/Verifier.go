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
	pubBytes, err := hex.DecodeString(resp.PubKey)
	if err != nil {
		return fmt.Errorf("invalid pubkey hex: %w", err)
	}
	sigBytes, err := hex.DecodeString(resp.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}

	msg, err := BLS_Signer.CanonicalVoteMessageV3(chainID, height, bindings, consensusHash, vote)
	if err != nil {
		return fmt.Errorf("build block-bound vote message: %w", err)
	}
	if blssign.BLSVerify(pubBytes, msg, sigBytes) != nil {
		return fmt.Errorf("bls verify (block-bound) failed for peer %s", resp.PeerID)
	}
	return nil
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

// VerifyAll verifies each response independently.
// Returns a map of peerID -> valid (true/false) and the first error encountered, if any.
func VerifyAll(responses []BLS_Signer.BLSresponse, vote int8) (map[string]bool, error) {
	results := make(map[string]bool, len(responses))
	var firstErr error
	for _, r := range responses {
		if err := Verify(r, vote); err != nil {
			results[r.PeerID] = false
			if firstErr == nil {
				firstErr = err
			}
		} else {
			results[r.PeerID] = true
		}
	}
	return results, firstErr
}

// VerifyAggregated aggregates all signatures (must be for the same vote message)
// and verifies them against the corresponding public keys using fast aggregate verify.
func VerifyAggregated(responses []BLS_Signer.BLSresponse, vote int8) (bool, error) {
	if len(responses) == 0 {
		return false, fmt.Errorf("no responses to verify")
	}
	msg, err := messageForVote(vote)
	if err != nil {
		return false, err
	}

	// Decode all signatures and pubkeys
	sigs := make([][]byte, 0, len(responses))
	pubs := make([][]byte, 0, len(responses))
	for _, r := range responses {
		sigBytes, err := hex.DecodeString(r.Signature)
		if err != nil {
			return false, fmt.Errorf("invalid signature hex for %s: %w", r.PeerID, err)
		}
		pubBytes, err := hex.DecodeString(r.PubKey)
		if err != nil {
			return false, fmt.Errorf("invalid pubkey hex for %s: %w", r.PeerID, err)
		}
		sigs = append(sigs, sigBytes)
		pubs = append(pubs, pubBytes)
	}

	// Aggregate signatures then fast-verify
	aggSig, err := blssign.BLSAggregate(sigs...)
	if err != nil {
		return false, fmt.Errorf("aggregate failed: %w", err)
	}
	ok, err := blssign.BLSFastAggregateVerify(pubs, msg, aggSig)
	if err != nil {
		return false, err
	}
	return ok, nil
}
