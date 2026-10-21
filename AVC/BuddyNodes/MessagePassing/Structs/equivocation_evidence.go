package Structs

import (
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

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
	values := tally.AuthorizedVotesByPeer[peerID]
	recs := tally.Signatures[peerID]
	tasks := make([]tallySigTask, 0, len(values))
	for i, v := range values {
		if i >= len(recs) || avcvotes.IsUnsignedValidatorVote(recs[i]) {
			continue
		}
		tasks = append(tasks, tallySigTask{peerID: peerID, vote: v, rec: recs[i]})
	}
	ok := verifyTallySigTasksConcurrently(tasks, BLS_Signer.DomainChainID(), height, blockHash, "")
	seen := make(map[int8]bool, 2)
	for i, t := range tasks {
		if ok[i] {
			seen[t.vote] = true
		}
	}
	return len(seen) >= 2
}
