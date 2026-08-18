// Package consensushash defines the versioned canonical block-hash preimages.
//
// v2 (current, in Security.RecomputeBlockHashFromContents /
// messaging.RecomputeBlockHashFromTxs) hashes ONLY the concatenation of tx
// hashes: keccak(tx0 ‖ tx1 ‖ …). That has two consensus defects (audit CON-02):
//  1. every empty block hashes to the zero hash (all empty blocks collide);
//  2. the same tx set at a different height / parent / state produces the same
//     block hash, so a validly-signed block can be replayed at another height.
//
// So today block_hash is functionally a duplicate of txns_root, not a block id.
//
// v3 binds the full block identity — chain, height, parent, state, txns, time —
// under an explicit domain tag, mirroring VoteDomainVersionV3 (which already
// binds chain+height+hash for votes). Domain separation guarantees a v2 and a
// v3 hash can never collide even on identical inputs, and a future v4 is cheap.
//
// This package is intentionally dependency-light (only go-ethereum/common for
// the Hash/Address types and x/crypto/sha3 for keccak — both pure-Go, no CGO)
// so it carries no import cycle into config/ and is trivially unit-testable.
package consensushash

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"
)

// BlockHashV3Domain is the domain-separation tag for the v3 block-hash preimage.
const BlockHashV3Domain = "jmdn/blockhash/v3"

// BlockHashV3 computes the v3 canonical block hash. Every field is fixed-width
// big-endian, so the concatenation is unambiguous without delimiters. txnsRoot
// carries the transaction binding (derive it from CONTENTS via the safe path,
// Security.RecomputeBlockHashFromContents, never from claimed wire tx.Hash).
//
// Determinism: pure function of its inputs — no wall clock, no map iteration,
// no network. Every node computes the identical hash for the same block.
func BlockHashV3(chainID, blockNumber uint64, prevHash, stateRoot, txnsRoot common.Hash, timestamp int64) common.Hash {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(BlockHashV3Domain))

	var u8 [8]byte
	binary.BigEndian.PutUint64(u8[:], chainID)
	h.Write(u8[:])
	binary.BigEndian.PutUint64(u8[:], blockNumber)
	h.Write(u8[:])

	h.Write(prevHash[:])
	h.Write(stateRoot[:])
	h.Write(txnsRoot[:])

	binary.BigEndian.PutUint64(u8[:], uint64(timestamp))
	h.Write(u8[:])

	var out common.Hash
	h.Sum(out[:0])
	return out
}
