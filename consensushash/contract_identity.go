package consensushash

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"
)

// Deterministic contract ART identity (EVM apply-path P2).
//
// A newly-deployed contract needs a FastSync ART identity ordinal (Account.Nonce)
// for its ledger account. Ownership model (operator, 2026-08-18): the SEQUENCER
// derives this value and BROADCASTS it in the block (in the block-carried
// identity map, alongside recipient nonces); validators consume the block-carried
// value on the apply path — exactly like recipient-account creation, which has NO
// local-mint fallback (per-node minting historically forked the AccountSync diff
// fleet-wide).
//
// This function is that canonical derivation: the sequencer calls it to compute
// the ordinal it stamps into the block, and a validator MAY recompute it to VERIFY
// the block-carried value (the contract address is already CREATE-deterministic,
// so the derivation is identical on every node). The authoritative source on the
// apply path remains the block-carried value; this is the shared, verifiable rule
// for producing it — not a node-side mint.

// ContractIdentityDomain domain-separates the ART-identity preimage so it can
// never collide with another keccak use over the same 20 address bytes.
const ContractIdentityDomain = "jmdn/contract-art-id/v1"

// DeriveContractARTNonce returns a deterministic, non-zero FastSync ART identity
// ordinal for a contract account from its address. Every node computes the same
// value. Never returns 0 (the apply path's "no identity" sentinel).
//
// NOTE: this is a 64-bit digest of a 256-bit keccak, so distinct addresses can in
// principle collide (birthday-bound ~2^32 accounts). The AccountSync ART keys on
// this ordinal, so collisions must be validated against the diff before enabling
// contract deployment fleet-wide (operator-accepted residual of the derived
// scheme). A collision-free scheme would widen Account.Nonce or key the ART on
// the address itself.
func DeriveContractARTNonce(addr common.Address) uint64 {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(ContractIdentityDomain))
	h.Write(addr[:])
	var sum [32]byte
	h.Sum(sum[:0])
	n := binary.BigEndian.Uint64(sum[:8])
	if n == 0 {
		n = 1 // never the sentinel
	}
	return n
}
