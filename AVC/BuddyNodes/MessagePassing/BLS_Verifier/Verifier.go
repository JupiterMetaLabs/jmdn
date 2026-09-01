package BLS_Verifier

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

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

// VerifyForBlock verifies a response's signature against the v3 block-bound vote
// message: chain id + HEIGHT + block hash + vote. A signature that passes is an
// attestation for THIS block, on THIS chain, at THIS height.
//
// v1 (block-only) and v2 (chain but not height-bound) formats are NO LONGER
// accepted: the whole fleet emits v3, and accepting older formats reopens the
// cross-chain / cross-height replay window. There is
// exactly one accepted format now, so there is no downgrade path to disable.
func VerifyForBlock(resp BLS_Signer.BLSresponse, chainID, height uint64, bindings, consensusHash string, vote int8) error {
	pubBytes, err := hex.DecodeString(resp.PubKey)
	if err != nil {
		return fmt.Errorf("invalid pubkey hex: %w", err)
	}
	sigBytes, err := hex.DecodeString(resp.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}

	// v4 first when a consensus hash is present (block hash + consensus hash),
	// then fall back to v3 (block hash only) so a mixed fleet still verifies
	// stragglers still emitting v3 during the coordinated rollout.
	if strings.TrimSpace(consensusHash) != "" {
		if msgV4, err4 := BLS_Signer.CanonicalVoteMessageV4(chainID, height, bindings, consensusHash, vote); err4 == nil {
			if blssign.BLSVerify(pubBytes, msgV4, sigBytes) == nil {
				return nil
			}
		}
	}

	msgV3, err := BLS_Signer.CanonicalVoteMessageV3(chainID, height, bindings, vote)
	if err != nil {
		return fmt.Errorf("build v3 vote message: %w", err)
	}
	if blssign.BLSVerify(pubBytes, msgV3, sigBytes) == nil {
		return nil
	}
	return fmt.Errorf("bls verify (v4/v3 block-bound) failed for peer %s", resp.PeerID)
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
