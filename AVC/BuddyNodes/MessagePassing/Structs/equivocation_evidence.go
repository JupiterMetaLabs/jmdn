package Structs

import (
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config/PubSubMessages"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	avctypes "github.com/JupiterMetaLabs/avc/types"
)

// SignedEquivocation reports whether peerID has BLS-verified votes with two
// or more different values for (height, blockHash) in the vote CRDT.
//
// avcvotes.TallyBlock matches each vote to a signature record and checks the
// record's public key, but does not verify the signature itself. Vote records
// arrive through CRDT sync from other buddies, so a record carrying a peer's
// (public) key and a junk signature is enough to make that peer look like an
// equivocator. Only records whose signatures verify, and never unsigned
// records, count as evidence here.
func SignedEquivocation(c *avctypes.Controller, height uint64, blockHash, peerID string, authorized map[string]string) bool {
	if c == nil {
		return false
	}
	tally, err := avcvotes.TallyBlock(c, height, blockHash, authorized)
	if err != nil {
		return false
	}

	// VerifyForBlock requires a non-empty ConsensusHash: an equivocation
	// charge is itself a vote-signature check, held to the same bar as a
	// tally. Only the block-hash string is available here, not the block
	// itself, so the hash is sourced from this node's own consensus-message
	// cache — the same cache processVotesFromCRDT_v2 already trusts for the
	// identical reason (D-26c). A block this node has never seen cannot be
	// verified against, so that case fails closed (no evidence) rather than
	// an unbound check.
	cm := PubSubMessages.LookupConsensusMessageByBlockHash(blockHash)
	if cm == nil || cm.ZKBlock == nil {
		return false
	}
	consensusHash := cm.ZKBlock.ConsensusHashHex()

	values := tally.AuthorizedVotesByPeer[peerID]
	recs := tally.Signatures[peerID]
	tasks := make([]tallySigTask, 0, len(values))
	for i, v := range values {
		// An unsigned record never counts: its empty signature fails
		// VerifyForBlock below.
		if i >= len(recs) {
			continue
		}
		tasks = append(tasks, tallySigTask{peerID: peerID, vote: v, rec: recs[i]})
	}
	ok := verifyTallySigTasksConcurrently(tasks, BLS_Signer.DomainChainID(), height, blockHash, consensusHash)
	seen := make(map[int8]bool, 2)
	for i, t := range tasks {
		if ok[i] {
			seen[t.vote] = true
		}
	}
	return len(seen) >= 2
}
