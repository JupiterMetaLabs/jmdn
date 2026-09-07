package Sequencer

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/rs/zerolog/log"
)

// Stage-2 beacon genesis bootstrap.
//
// messaging.SelectEntropyCommittee seeds epoch E's reveal committee from
// ENTROPY-E, the value sealed from epoch E-1's reveals. The first live epoch
// has no E-1, and the code that owns that seam says so itself: "no such
// mechanism exists yet". Without one, installing the beacon on any chain -
// fresh or running - yields no committee, no reveals, no seal, and the first
// epoch-boundary block fails closed on ErrVDFProofNotReady.
//
// This file is that mechanism. For every epoch the operator PINS in
// consensus.entropy_bootstrap.epochs (config yaml, identical fleet-wide), a
// deterministic value is published into the BeaconSource at install time:
//
//	ENTROPY-E(bootstrap) = SHA256( domain || u64:chainID || field:authorityPin || field:seed || u64:E )
//
// using committee.WriteField / WriteU64 - the same length-prefixed encoding
// the rest of the seed derivations use. Binding to the pinned seed-authority
// key means two networks cannot share a bootstrap committee schedule by
// accident; Seed lets two devnets that share both still differ.
//
// Bootstrap epochs are also EXEMPT from sealing (vdf_seal_wiring.go) and
// from the boundary-block proof requirement (Block/consensus_fields.go): no
// VDF ran for them, so there is no proof, and a real seal landing on a
// bootstrapped epoch would be refused by BeaconSource.Publish (differing
// entropy) and turn into a boundary-block 503. Pinning the set in config -
// rather than deriving it from the slot a node happened to install at - is
// what makes every node agree on which epochs those are.
//
// SECURITY: bootstrap entropy is public and computable by anyone in advance.
// It is grindable by construction and provides none of the RANDAO+VDF
// guarantees. It governs ONLY the listed epochs; real entropy takes over at
// the first epoch after them. It is logged as a standing finding at install.

// EntropyBootstrapDomain domain-separates bootstrap entropy from every other
// hash in this codebase (DeriveSeed, EntropyCommitteeSeed, vote domains).
const EntropyBootstrapDomain = "jmdt/entropy-bootstrap/v1"

// ErrBootstrapNeedsAuthorityPin: bootstrap is configured but there is no
// pinned seed-authority key to bind it to.
var ErrBootstrapNeedsAuthorityPin = errors.New("entropy: consensus.entropy_bootstrap.epochs is set but consensus.seed_authority_bls_pub is empty - the bootstrap value must be bound to the network's authority pin")

var (
	bootstrapEpochsMu sync.RWMutex
	bootstrapEpochs   = map[uint64]struct{}{}
)

// BootstrapEntropy derives the deterministic ENTROPY-E for a bootstrap epoch.
// Pure; every node with the same inputs derives the same 32 bytes.
func BootstrapEntropy(chainID uint64, authorityPin, seed string, epoch uint64) []byte {
	h := sha256.New()
	committee.WriteField(h, []byte(EntropyBootstrapDomain))
	committee.WriteU64(h, chainID)
	committee.WriteField(h, []byte(strings.ToLower(strings.TrimSpace(authorityPin))))
	committee.WriteField(h, []byte(seed))
	committee.WriteU64(h, epoch)
	return h.Sum(nil)
}

// publishBootstrapEntropy publishes the bootstrap value for every listed epoch
// into sink and records the set for IsBootstrapEpoch. Epochs are published in
// ascending order so BeaconSource's retention eviction (newest - retain)
// cannot drop a later-listed lower epoch. Fails closed on the first Publish
// error - a partial bootstrap set is worse than none, because the nodes that
// got further would seat different committees.
func publishBootstrapEntropy(sink *committee.BeaconSource, chainID uint64, authorityPin, seed string, epochs []uint64) error {
	if len(epochs) == 0 {
		return nil
	}
	if strings.TrimSpace(authorityPin) == "" {
		return ErrBootstrapNeedsAuthorityPin
	}
	sorted := append([]uint64(nil), epochs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	published := make([]uint64, 0, len(sorted))
	for i, e := range sorted {
		if i > 0 && sorted[i-1] == e {
			continue // duplicate in config; harmless
		}
		if err := sink.Publish(e, BootstrapEntropy(chainID, authorityPin, seed, e)); err != nil {
			return fmt.Errorf("entropy: publishing bootstrap ENTROPY-%d: %w", e, err)
		}
		published = append(published, e)
	}

	bootstrapEpochsMu.Lock()
	for _, e := range published {
		bootstrapEpochs[e] = struct{}{}
	}
	bootstrapEpochsMu.Unlock()

	log.Warn().Uints64("epochs", published).Uint64("chain_id", chainID).
		Msg("entropy: SECURITY - bootstrap entropy published for the listed epochs (consensus.entropy_bootstrap). " +
			"These values are public and grindable and carry NO RANDAO+VDF guarantee; they exist only to start " +
			"the reveal->seal relay. Real entropy governs from the first epoch after them. Sealing and the " +
			"boundary-block proof are skipped for these epochs on every node")
	return nil
}

// IsBootstrapEpoch reports whether epoch's entropy was bootstrapped on this
// node (and therefore, by config pinning, on every node).
func IsBootstrapEpoch(epoch uint64) bool {
	bootstrapEpochsMu.RLock()
	defer bootstrapEpochsMu.RUnlock()
	_, ok := bootstrapEpochs[epoch]
	return ok
}

// resetBootstrapEpochs clears the recorded set. Test helper; production
// installs once per process.
func resetBootstrapEpochs() {
	bootstrapEpochsMu.Lock()
	bootstrapEpochs = map[uint64]struct{}{}
	bootstrapEpochsMu.Unlock()
}
