package messaging

// Retention of finalised RANDAO mixes — the missing input for inbound VDF
// proof adoption (Architecture §F / beacon.Pipeline.Accept).
//
// # Why this file has to exist
//
// beacon.Pipeline.Accept(forEpoch, mix, proof) verifies a peer's proof by
// RE-DERIVING the VDF challenge from the mix. It does not, and must not, take
// the mix from the block: a mix supplied by the same party that supplied the
// proof would verify any proof that party chose, which is precisely the
// steering Accept exists to prevent. The verifier has to hold the mix itself.
//
// Before this file, no node held it. notifyEpochFinalised handed the mix to
// the Stage-E sealing hook and dropped it — the value existed for the duration
// of one function call. A node that had not sealed the epoch itself therefore
// had no way to verify anybody else's proof, which is why Pipeline.Accept had
// zero callers: not an oversight in the wiring, a missing prerequisite.
//
// # What is stored, and what is NOT
//
// Only mixes this node finalised ITSELF, from its own accumulator or its own
// fallback fold (decideEpoch / resolvePendingFallbacks). A mix is never taken
// from the network — being the independent half of the verification is the
// whole point.
//
// Retention is bounded and tiny: a mix is only useful while a proof for the
// epoch after it can still arrive, so it is kept for about as long as
// committee.BeaconSource retains entropy.

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/randao"
)

// configuredRetainEpochs reads JMDN_AVC_BEACON_RETAIN_EPOCHS — the SAME env
// var Sequencer/beacon_install.go's InstallAVCBeaconFromEnv and
// Sequencer/vdf_seal_wiring.go's configuredBeaconRetainEpochs already read
// (same default, same parse rule). Duplicated here, not shared, because
// messaging cannot import Sequencer (Sequencer imports messaging, not the
// reverse) — same constraint vdf_seal_wiring.go's copy documents. Error-
// tolerant like that copy, not error-returning like beacon_install.go's: this
// is retention bookkeeping consulted on every remember()/
// entropyAccumulatorFor() call on an already-running node, which must never
// abort over a malformed value beacon_install.go already validated at
// startup. D-47: this file previously ignored the env var entirely via a
// hardcoded mixRetainEpochs constant, so JMDN_AVC_BEACON_RETAIN_EPOCHS moved
// the beacon's own retention but not this store's.
func configuredRetainEpochs() uint64 {
	retain := uint64(committee.MinRetainedEpochs)
	if retainStr := strings.TrimSpace(os.Getenv("JMDN_AVC_BEACON_RETAIN_EPOCHS")); retainStr != "" {
		if r, err := strconv.ParseUint(retainStr, 10, 64); err == nil {
			retain = r
		}
	}
	return retain
}

// mixRetainEpochs bounds the store. It mirrors the configured beacon
// retention for the same reason committee.MinRetainedEpochs exists — a
// legitimately-lagging peer's boundary block must still be verifiable — plus
// one, because a proof for epoch E is checked against the mix of E-1.
func mixRetainEpochs() uint64 {
	return configuredRetainEpochs() + 1
}

type mixStore struct {
	mu     sync.RWMutex
	mixes  map[uint64]randao.Seed
	newest uint64
	seeded bool
}

var defaultMixStore = &mixStore{mixes: make(map[uint64]randao.Seed)}

// remember records the mix an epoch finalised to.
//
// Idempotent for an identical value and REFUSED for a conflicting one — the
// same rule committee.BeaconSource.Publish enforces. An epoch's mix is final;
// silently replacing it would change which proofs verify after the fact. A
// conflict means this node finalised one epoch twice with different inputs,
// which is a bug worth surfacing rather than smoothing over.
func (m *mixStore) remember(epoch uint64, seed randao.Seed) (ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if prev, exists := m.mixes[epoch]; exists {
		return prev == seed
	}
	m.mixes[epoch] = seed
	if !m.seeded || epoch > m.newest {
		m.newest = epoch
		m.seeded = true
	}
	retain := mixRetainEpochs()
	if m.newest >= retain {
		cutoff := m.newest - retain
		for e := range m.mixes {
			if e < cutoff {
				delete(m.mixes, e)
			}
		}
	}
	return true
}

func (m *mixStore) get(epoch uint64) (randao.Seed, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.mixes[epoch]
	return s, ok
}

// FinalisedMixFor returns the mix this node finalised for closedEpoch.
//
// ok=false means this node never finalised that epoch — it was down, still
// syncing, or the epoch has aged out of retention. In that case it cannot
// verify a proof sealed from that mix and must say so rather than guess.
func FinalisedMixFor(closedEpoch uint64) (randao.Seed, bool) {
	return defaultMixStore.get(closedEpoch)
}

// RememberFinalisedMixForTest seeds the store directly. Test-only.
func RememberFinalisedMixForTest(epoch uint64, seed randao.Seed) bool {
	return defaultMixStore.remember(epoch, seed)
}

// ResetMixStoreForTest clears the store. Test-only.
func ResetMixStoreForTest() {
	defaultMixStore.mu.Lock()
	defaultMixStore.mixes = make(map[uint64]randao.Seed)
	defaultMixStore.newest = 0
	defaultMixStore.seeded = false
	defaultMixStore.mu.Unlock()
}
