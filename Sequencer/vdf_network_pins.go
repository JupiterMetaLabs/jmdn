package Sequencer

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/JupiterMetaLabs/avc/vdf"

	"gossipnode/config/settings"
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

// networkPinPolicy declares what a network pin is allowed to be used for.
//
// EVERY entry in networkVDFPins must have one. A pin with no policy is refused
// at install (enforceNetworkPinChainPolicy), so forgetting to declare one fails
// CLOSED — the alternative, treating "undeclared" as "unrestricted", is how a
// devnet modulus reaches mainnet.
type networkPinPolicy struct {
	// TrapdoorKnown records that the modulus's factorisation was known to
	// whoever generated it.
	//
	// This is not a quality judgement, it is a capability statement: knowing
	// p and q gives phi(N), and phi(N) turns the VDF's T sequential squarings
	// into a single modular exponentiation. The delay simply does not exist
	// for that party. They can compute an epoch's entropy before the reveal
	// window closes, see which committee it seats, and withhold or re-grind
	// their own reveal until the draw favours them — producing proofs that
	// verify perfectly, because they ARE valid. Just not slow.
	TrapdoorKnown bool

	// AllowedChainIDs restricts the pin to specific networks.
	//
	// Required when TrapdoorKnown is set. Empty means unrestricted and is only
	// legal for a pin nobody holds a trapdoor for.
	AllowedChainIDs []uint64
}

// networkPinPolicies is the policy table. Keys must match networkVDFPins
// exactly; TestEveryNetworkPinHasAPolicy enforces that.
var networkPinPolicies = map[string]networkPinPolicy{
	"rsa-2048-testnet-ephemeral": {
		TrapdoorKnown:   true,
		AllowedChainIDs: []uint64{8000800}, // jmdt devnet (jmdt-devnet/.env JMDN_CHAIN_ID)
	},
}

// currentChainID reports the configured network chain id, and whether it is
// KNOWN — the second return is the whole point.
//
// settings' compiled default Network.ChainID is 8000800, which is also the
// devnet's chain id. Substituting that default when settings have not loaded
// would make an unconfigured mainnet node look exactly like the devnet and
// wave through the one modulus this guard exists to stop. So an unloaded
// config reports "unknown" and the caller refuses.
//
// A var so tests can drive the policy without mutating global settings.
var currentChainID = func() (uint64, bool) {
	if !settings.IsLoaded() {
		return 0, false
	}
	return uint64(settings.Get().Network.ChainID), true
}

// ErrNetworkPinChainNotAllowed reports a network pin used on a chain its policy
// does not list.
var ErrNetworkPinChainNotAllowed = errors.New("entropy: this VDF modulus is not permitted on this chain")

// enforceNetworkPinChainPolicy is the D-29 guard: it refuses a trapdoored
// modulus anywhere its policy does not explicitly allow.
//
// Before this existed, the only thing keeping rsa-2048-testnet-ephemeral off
// mainnet was the sentence "Never ship this group name in a mainnet config" in
// a comment. Comments do not survive a copied .env; a startup check does.
func enforceNetworkPinChainPolicy(name string) error {
	pol, declared := networkPinPolicies[name]
	if !declared {
		return fmt.Errorf("%w: network pin %q has no declared policy in "+
			"Sequencer/vdf_network_pins.go, so its trust assumptions are unknown and it is "+
			"refused. Declare TrapdoorKnown and AllowedChainIDs for it",
			ErrNetworkPinChainNotAllowed, name)
	}
	if !pol.TrapdoorKnown && len(pol.AllowedChainIDs) == 0 {
		return nil // no trapdoor holder, no chain restriction
	}

	chainID, known := currentChainID()
	if !known {
		return fmt.Errorf("%w: %q requires a known chain id and settings are not loaded. "+
			"This node refuses rather than assuming a default — the compiled default chain id "+
			"is the devnet's, so assuming it would silently permit this modulus on any network",
			ErrNetworkPinChainNotAllowed, name)
	}
	for _, allowed := range pol.AllowedChainIDs {
		if chainID == allowed {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is pinned for chain(s) %v but this node is on chain %d. "+
		"TrapdoorKnown=%t — whoever generated this modulus knows its factorisation and can "+
		"evaluate the VDF instantly, which lets them grind committee selection every epoch "+
		"while producing proofs that verify. Use a sourced modulus (rsa-2048-frc) or a class "+
		"group on this network",
		ErrNetworkPinChainNotAllowed, name, pol.AllowedChainIDs, chainID, pol.TrapdoorKnown)
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
	// D-29 chain policy FIRST. A modulus barred on this network should be
	// refused for that reason, not for a digest mismatch discovered later —
	// the operator needs to be told the actual problem.
	if err := enforceNetworkPinChainPolicy(name); err != nil {
		return nil, err
	}
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
