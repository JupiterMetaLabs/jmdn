package messaging

// The entropy cut-over (A-1, A-2): epochs before it keep the old rules
// (Ed25519 reveals, legacy VDF) so a running chain's history still verifies;
// epochs from it use BLS reveals and the fixed VDF. A chain not listed uses the
// new rules from epoch 0. Every node must be upgraded before its chain's
// cut-over. Delete this file and the legacy paths once no node needs them.

import BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"

// entropyCutoverByChain maps chain ID to its cut-over epoch (epoch E starts at
// slot E*50).
var entropyCutoverByChain = map[uint64]uint64{
	8000800: 60, // JMDT testnet: slot 3000, after anchoring (height 2560)
}

// entropyCutoverFn is a variable only so tests can place the cut-over.
var entropyCutoverFn = func() uint64 {
	return entropyCutoverByChain[BLS_Signer.DomainChainID()]
}

// afterEntropyCutover reports whether epoch's reveals are BLS (true) or Ed25519.
func afterEntropyCutover(epoch uint64) bool { return epoch >= entropyCutoverFn() }

// VDFCutoverEpoch is the first epoch whose entropy is sealed with the fixed
// VDF. ENTROPY(E) is sealed from mix(E-1), so it is one after the cut-over.
func VDFCutoverEpoch() uint64 { return entropyCutoverFn() + 1 }
