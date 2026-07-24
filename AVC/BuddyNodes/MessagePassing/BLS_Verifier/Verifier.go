package BLS_Verifier

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// AcceptV1BlockBoundVotes controls whether the verifier still accepts legacy v1
// (block-bound but NOT chain-id-bound) vote signatures during the P4 staged
// rollout. Default ON so a mixed fleet keeps reaching quorum while emitters
// migrate to v2. Set JMDN_ACCEPT_V1_VOTES=0 once every emitter signs v2 to
// close the cross-chain replay window that v1 leaves open.
var AcceptV1BlockBoundVotes = os.Getenv("JMDN_ACCEPT_V1_VOTES") != "0"

// AcceptV2BlockBoundVotes controls whether the verifier still accepts v2
// (chain-id-bound but NOT height-bound) vote signatures during the A2 staged
// rollout. Default ON so a mixed fleet reaches quorum while emitters migrate to
// v3. Set JMDN_ACCEPT_V2_VOTES=0 once every emitter signs v3 to close the
// same-body-replay-at-another-height window that v2 leaves open.
var AcceptV2BlockBoundVotes = os.Getenv("JMDN_ACCEPT_V2_VOTES") != "0"

// canonicalVoteMessageV1 rebuilds the LEGACY (pre-P4) block-bound message
// "zkvote:<blockhash>:<vote>" — block-bound but with no chain-id domain
// separation. Retained only so VerifyForBlock can accept in-flight v1
// signatures during rollout; gated by AcceptV1BlockBoundVotes.
func canonicalVoteMessageV1(bindings string, vote int8) ([]byte, error) {
	if vote != -1 && vote != 1 {
		return nil, fmt.Errorf("invalid vote: %d", vote)
	}
	bindings = strings.ToLower(strings.TrimSpace(bindings))
	if bindings == "" {
		return nil, fmt.Errorf("empty block bindings")
	}
	return []byte(BLS_Signer.BlockBoundVotePrefix + bindings + ":" + strconv.Itoa(int(vote))), nil
}

// messageForVote returns the canonical message bytes used for signing a vote
func messageForVote(vote int8) ([]byte, error) {
	if vote != -1 && vote != 1 {
		return nil, fmt.Errorf("invalid vote: %d", vote)
	}
	return []byte("vote:" + strconv.Itoa(int(vote))), nil
}

// CanonicalBlockVoteMessage returns the canonical bytes for a v2 vote bound to a
// specific block AND chain id. It delegates to BLS_Signer.CanonicalVoteMessage
// so there is exactly one definition of the format shared by signer and
// verifier (no drift). `bindings` must uniquely identify the block (block hash
// hex).
func CanonicalBlockVoteMessage(chainID uint64, bindings string, vote int8) ([]byte, error) {
	return BLS_Signer.CanonicalVoteMessage(chainID, bindings, vote)
}

// VerifyForBlock verifies a response's signature against a block-bound vote
// message. A signature that passes here is provably an attestation for THIS
// block, on THIS chain, at THIS height, and cannot be replayed onto another
// block (JMDN-001 / D3), chain/fork (P4), or height (A2). Version precedence
// (accept newest-bound first, older only during staged rollout):
//   - v3: chain + HEIGHT + block + vote (tried when height > 0)
//   - v2: chain + block + vote        (accepted when AcceptV2BlockBoundVotes)
//   - v1: block + vote                (accepted when AcceptV1BlockBoundVotes)
func VerifyForBlock(resp BLS_Signer.BLSresponse, chainID, height uint64, bindings string, vote int8) error {
	pubBytes, err := hex.DecodeString(resp.PubKey)
	if err != nil {
		return fmt.Errorf("invalid pubkey hex: %w", err)
	}
	sigBytes, err := hex.DecodeString(resp.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}

	// v3: chain-id + height-bound (current). Only when a height is known.
	if height > 0 {
		if msgV3, err := BLS_Signer.CanonicalVoteMessageV3(chainID, height, bindings, vote); err == nil {
			if blssign.BLSVerify(pubBytes, msgV3, sigBytes) == nil {
				return nil
			}
		}
	}

	// v2: chain-id-bound (no height), accepted during the A2 rollout.
	if AcceptV2BlockBoundVotes {
		if msgV2, err := CanonicalBlockVoteMessage(chainID, bindings, vote); err == nil {
			if blssign.BLSVerify(pubBytes, msgV2, sigBytes) == nil {
				return nil
			}
		}
	}

	// v1: legacy block-bound-only, accepted only during staged rollout.
	if AcceptV1BlockBoundVotes {
		if msgV1, err := canonicalVoteMessageV1(bindings, vote); err == nil {
			if blssign.BLSVerify(pubBytes, msgV1, sigBytes) == nil {
				return nil
			}
		}
	}

	return fmt.Errorf("bls verify (block-bound) failed for peer %s", resp.PeerID)
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
