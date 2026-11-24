package Structs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/config/PubSubMessages"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	avctypes "github.com/JupiterMetaLabs/avc/types"
)

// A-3 (2026-10-22 audit; D-52). avc's TallyBlock keeps ONE signature record
// per (peer, vote): the first in sorted element order. A record carrying a
// peer's ID with a junk signature or key sorts ahead of the genuine one (an
// empty signature sorts before any hex digit), so a single buddy that merges
// such a record through CRDT sync makes every other buddy drop that peer's
// genuine vote, either at TallyBlock's key check or at verifyTallySignatures.
// Merged junk could also fill the per-peer cap and lock the genuine record
// out. The helpers here verify a record exactly the way the tally does, so:
//
//   - rescueShadowedVotes recovers a (peer, vote) pair the tally dropped when
//     ANY candidate record for it verifies, and
//   - SigMergeVerifier refuses to store a merged record that would not verify.
//
// Both use the same check, against the same voter set, so neither can drop a
// record the other would accept.

// parseSigElement parses one "<peerID>:<json VoteRecord>" votesig: element
// exactly as avc's parseSigElements does, including its rule that the element
// prefix and the record's own PeerID must agree.
func parseSigElement(elem string) (string, avcvotes.VoteRecord, bool) {
	peerID, jsonPart, found := strings.Cut(elem, ":")
	if !found {
		return "", avcvotes.VoteRecord{}, false
	}
	var rec avcvotes.VoteRecord
	if err := json.Unmarshal([]byte(jsonPart), &rec); err != nil {
		return "", avcvotes.VoteRecord{}, false
	}
	if rec.PeerID != peerID {
		return "", avcvotes.VoteRecord{}, false
	}
	return peerID, rec, true
}

// verifyVoteRecord reports whether rec is a genuine vote for this block: the
// same matching rule as avc's findMatchingRecord, the same key binding as its
// isAuthorizedVote, and the same BLS check as verifyTallySignatures.
func verifyVoteRecord(rec avcvotes.VoteRecord, height uint64, blockHash, consensusHash string, authorized map[string]string) bool {
	if rec.BlockHash != blockHash || rec.Height != height || (rec.Vote != 1 && rec.Vote != -1) {
		return false
	}
	want, ok := authorized[rec.PeerID]
	if !ok || want == "" || strings.ToLower(strings.TrimSpace(rec.BLSPubKeyHex)) != want {
		return false
	}
	resp := BLS_Signer.BLSresponse{PeerID: rec.PeerID, PubKey: rec.BLSPubKeyHex, Signature: rec.BLSSignature}
	return BLS_Verifier.VerifyForBlock(resp, BLS_Signer.DomainChainID(), height, blockHash, consensusHash, rec.Vote) == nil
}

// rescueShadowedVotes adds back every (peer, vote) pair that has a vote
// element in the block's CRDT but is missing from verified, provided some
// candidate signature record for it verifies. In honest operation nothing is
// missing except write-order races (vote landed before its signature), so the
// signature set is only read when there is something to rescue.
//
// verified is extended in place, keeping AuthorizedVotesByPeer and Signatures
// in lockstep. Elements are visited in sorted order, so the result is the same
// on every node for the same CRDT contents. A non-empty onlyPeer limits the
// rescue to that one peer.
func rescueShadowedVotes(c *avctypes.Controller, verified *avcvotes.BlockTally, height uint64, blockHash, consensusHash string, authorized map[string]string, onlyPeer string) (rescued int) {
	if c == nil || c.CRDTLayer == nil {
		return 0
	}
	voteElems, _ := c.CRDTLayer.GetSet(avcvotes.BlockVoteKey(height, blockHash))

	type pair struct {
		peerID string
		vote   int8
	}
	var missing []pair
	for _, elem := range voteElems {
		peerID, voteStr, found := strings.Cut(elem, ":")
		if !found || (onlyPeer != "" && peerID != onlyPeer) {
			continue
		}
		v64, err := strconv.ParseInt(voteStr, 10, 8)
		if err != nil || (v64 != 1 && v64 != -1) {
			continue
		}
		vote := int8(v64)
		if hasVote(verified.AuthorizedVotesByPeer[peerID], vote) {
			continue
		}
		missing = append(missing, pair{peerID, vote})
	}
	if len(missing) == 0 {
		return 0
	}

	sigElems, _ := c.CRDTLayer.GetSet(avcvotes.BlockSigKey(height, blockHash))
	candidates := make(map[string][]avcvotes.VoteRecord)
	for _, elem := range sigElems {
		if peerID, rec, ok := parseSigElement(elem); ok {
			candidates[peerID] = append(candidates[peerID], rec)
		}
	}

	for _, p := range missing {
		for _, rec := range candidates[p.peerID] {
			if rec.Vote != p.vote || !verifyVoteRecord(rec, height, blockHash, consensusHash, authorized) {
				continue
			}
			verified.AuthorizedVotesByPeer[p.peerID] = append(verified.AuthorizedVotesByPeer[p.peerID], p.vote)
			verified.Signatures[p.peerID] = append(verified.Signatures[p.peerID], rec)
			rescued++
			break
		}
	}
	return rescued
}

func hasVote(values []int8, v int8) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

// SigMergeVerifier checks votesig: elements arriving through buddy CRDT sync
// against one block's voter set and ConsensusHash, resolved once per key.
type SigMergeVerifier struct {
	height        uint64
	blockHash     string
	consensusHash string
	authorized    map[string]string
}

// NewSigMergeVerifier resolves what verification needs for the block a
// votesig: key belongs to. It returns an error when that is not possible here
// (not a votesig: key, voter set unavailable, or this node has not seen the
// block yet); the caller then keeps its previous, unverified behaviour, and
// rescueShadowedVotes still protects the tally.
func NewSigMergeVerifier(key string) (*SigMergeVerifier, error) {
	height, blockHash, ok := splitSigKey(key)
	if !ok {
		return nil, fmt.Errorf("not a votesig key: %q", key)
	}
	authorized, err := authorizedVotersFor(height)
	if err != nil {
		return nil, fmt.Errorf("voter set for height %d: %w", height, err)
	}
	cm := PubSubMessages.LookupConsensusMessageByBlockHash(blockHash)
	if cm == nil || cm.ZKBlock == nil {
		return nil, fmt.Errorf("no local block for %s", blockHash)
	}
	return &SigMergeVerifier{height: height, blockHash: blockHash, consensusHash: cm.ZKBlock.ConsensusHashHex(), authorized: authorized}, nil
}

// Verify reports whether a merged votesig: element is a genuine vote record
// for this verifier's block.
func (m *SigMergeVerifier) Verify(elem string) bool {
	_, rec, ok := parseSigElement(elem)
	return ok && verifyVoteRecord(rec, m.height, m.blockHash, m.consensusHash, m.authorized)
}

// splitSigKey splits "votesig:<height>:<blockHash>" into its height and hash.
func splitSigKey(key string) (uint64, string, bool) {
	rest, ok := strings.CutPrefix(key, "votesig:")
	if !ok {
		return 0, "", false
	}
	return splitHeightHash(rest)
}

// splitHeightHash splits the "<height>:<blockHash>" part of a block vote key.
func splitHeightHash(rest string) (uint64, string, bool) {
	heightStr, blockHash, found := strings.Cut(rest, ":")
	if !found || blockHash == "" {
		return 0, "", false
	}
	height, err := strconv.ParseUint(heightStr, 10, 64)
	if err != nil {
		return 0, "", false
	}
	return height, blockHash, true
}
