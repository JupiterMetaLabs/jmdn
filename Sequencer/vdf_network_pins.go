package Sequencer

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/JupiterMetaLabs/avc/vdf"
)

// Network-owned VDF modulus pins.
//
// avc/vdf's registry is a LIBRARY allowlist: it may only carry moduli with a
// generically-citable primary source (rsa-2048-frc). A modulus that exists for
// one network — a testnet's operator-generated throwaway, a ceremony output
// for a specific deployment — is this node distribution's policy, and pinning
// it belongs here, not in the library that every consumer imports. Keeping
// the two apart means avc never ships a devnet trapdoor as if it were a
// sourced constant, and jmdn's operators decide what their fleets accept.
//
// Semantics are identical to vdf.NewPinnedRSAGroup: the record's published
// dimensions are enforced, and the modulus MUST match the pinned digest
// (sha256 over big.Int.Bytes(), the same preimage vdf.ModulusDigest uses, so
// `go run ./cmd/vdfpin < modulus.txt` in the avc repo reproduces it). A
// network pin is consulted only after the library registry has refused the
// name, and is treated as fully pinned — no JMDN_AVC_VDF_ALLOW_UNPINNED_MODULUS
// override is needed for a matching modulus, and none is granted for a
// mismatching one under a name listed here.
//
// Adding an entry is a deliberate act: record how N was produced, and be
// honest in Note about what the trust assumption is. Never list a mainnet
// modulus here that is not ALSO independently sourced.
var networkVDFPins = map[string]vdf.ProvenanceRecord{
	"rsa-2048-testnet-ephemeral": {
		Name:   "rsa-2048-testnet-ephemeral",
		Bits:   vdf.RSA2048Bits,
		Digits: vdf.RSA2048Digits,
		Digest: "a48337dd135615a9ee5c47dee0dcecca6558500151fa361b0beaf717469b778e",
		Source: "OPERATOR-GENERATED throwaway modulus (openssl genpkey RSA-2048) for the " +
			"jmdt devnet/testnet. The generator knew the factors p,q at creation; the " +
			"private key was shredded, but this is NOT a trusted setup and NOT trapdoor-free.",
		Note: "INSECURE BY CONSTRUCTION — devnet/testnet pipeline validation only. Whoever " +
			"generated N can evaluate the VDF instantly and grind committee selection. " +
			"MUST be replaced by rsa-2048-frc (sourced) or a class group before any " +
			"adversarial network or mainnet. Never ship this group name in a mainnet config.",
	},
}

// ErrNetworkPinMismatch reports a modulus supplied under a network-pinned name
// whose digest is not the pinned one.
var ErrNetworkPinMismatch = errors.New("entropy: modulus does not match the digest pinned for this group name in jmdn's network pins")

// lookupNetworkPin returns jmdn's own pin for a group name, if any.
func lookupNetworkPin(name string) (vdf.ProvenanceRecord, bool) {
	rec, ok := networkVDFPins[name]
	return rec, ok
}

// newNetworkPinnedRSAGroup builds a group from a modulus pinned by THIS node
// distribution. Same three fail-closed checks as vdf.NewPinnedRSAGroup, in the
// same order: mathematical validity, published shape, pinned digest.
func newNetworkPinnedRSAGroup(n *big.Int, name string) (vdf.Group, error) {
	rec, ok := lookupNetworkPin(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q has no jmdn network pin either", vdf.ErrUnknownProvenance, name)
	}
	if !rec.Pinned() {
		return nil, fmt.Errorf("%w: jmdn network pin %q has an empty digest", vdf.ErrProvenanceNotPinned, name)
	}
	if err := vdf.ValidateModulus(n); err != nil {
		return nil, err
	}
	if err := vdf.ValidateChallengeShape(n, rec.Bits, rec.Digits); err != nil {
		return nil, fmt.Errorf("%w (expected the published dimensions of network pin %q, from: %s)",
			err, rec.Name, rec.Source)
	}
	got, err := vdf.ModulusDigest(n)
	if err != nil {
		return nil, err
	}
	if got != rec.Digest {
		return nil, fmt.Errorf("%w: %q got sha256 %s, pinned %s. Either the wrong modulus.txt "+
			"was supplied or the pin in Sequencer/vdf_network_pins.go is stale",
			ErrNetworkPinMismatch, name, got, rec.Digest)
	}
	return vdf.NewRSAGroup(n, name)
}
