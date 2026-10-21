// Package sequencerid resolves the fleet sequencer's libp2p peer ID — the
// identity validators require on vote requests, finalized blocks and L1-commit
// gossip.
//
// Sources, in order:
//  1. consensus.sequencer_pinned_peer_id (operator config). When set it wins.
//  2. The sequencer peer ID carried in the latest verified current-epoch
//     committee snapshot (signed by the pinned seed authority key, v3 format),
//     recorded by the seed committee source via SetFromSeed.
//
// When both are present and disagree, the config value is used and the
// mismatch is logged on every snapshot refresh.
//
// Callers keep the existing contract: an empty result means "not known —
// unenforced (warn)". Under production posture an unknown sequencer is
// returned as a value that matches no peer, so every sender check fails closed
// instead of opening up.
package sequencerid

import (
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"gossipnode/config/settings"
)

// Unknown is returned under production posture when no sequencer is known. It
// is non-empty (so checks are enforced) and is not a valid peer ID (so it
// matches nobody).
const Unknown = "unknown-sequencer"

var (
	mu        sync.RWMutex
	seedID    string
	seedEpoch uint64
)

// SetFromSeed records the sequencer peer ID from a verified current-epoch
// committee snapshot. An older epoch than the one already recorded is
// ignored. An empty id (a v2 snapshot) clears the seed value for that epoch.
func SetFromSeed(id string, epoch uint64) {
	id = strings.TrimSpace(id)
	mu.Lock()
	if epoch < seedEpoch {
		mu.Unlock()
		return
	}
	changed := id != seedID
	seedID, seedEpoch = id, epoch
	mu.Unlock()

	if cfg := configPin(); cfg != "" && id != "" && cfg != id {
		log.Error().Str("config_pin", cfg).Str("seed_sequencer", id).Uint64("epoch", epoch).
			Msg("sequencer identity mismatch: consensus.sequencer_pinned_peer_id differs from the seed-signed committee snapshot; using the config pin")
	} else if changed && id != "" {
		log.Info().Str("sequencer", id).Uint64("epoch", epoch).
			Msg("sequencer identity learned from the seed-signed committee snapshot")
	}
}

// FromSeed returns the seed-provided sequencer peer ID ("" if none).
func FromSeed() string {
	mu.RLock()
	defer mu.RUnlock()
	return seedID
}

// Get returns the sequencer peer ID to enforce. See the package doc.
func Get() string {
	if p := configPin(); p != "" {
		return p
	}
	if s := FromSeed(); s != "" {
		return s
	}
	if productionPosture() {
		return Unknown
	}
	return ""
}

func configPin() string {
	if !settings.IsLoaded() {
		return ""
	}
	return strings.TrimSpace(settings.Get().Consensus.SequencerPinnedPeerID)
}

func productionPosture() bool {
	// settings.ProductionPosture is the single definition of "production"
	// (strict posture, or a mainnet environment, folding case and trimming).
	// Repeating the comparison is how it drifts: a raw != "mainnet" elsewhere
	// was case-sensitive and would have read "Mainnet" as non-production.
	if !settings.IsLoaded() {
		return false
	}
	return settings.Get().ProductionPosture()
}
