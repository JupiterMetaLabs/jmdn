package messaging

// SelectEntropyCommittee — Architecture §4.7 (formula), §4.6 (m, why),
// AVC-M4-Entropy-Reveal-Pipeline-Design.md §A.
//
// Picks the m=EntropyCommitteeSize revealers responsible for epoch E's
// RANDAO commit-reveal. This is Stage A of the M4 pipeline: the piece that
// decides WHO reveals, not the reveal/fold/finalise machinery itself (§B-F
// of the design doc — §B built, §C-F not yet).
//
// CORRECTED 2026-08-19, same day as the first version: §4.7 was not yet
// read when SelectEntropyCommittee was first written, and two things in
// that first version contradicted it once it was. Both are fixed here.
// Recorded so the mistake and the fix travel together, not just the fix:
//
//  1. Hash field order. §4.7's formula is
//     entropySeed_E = SHA256(domain ‖ chainID ‖ ENTROPY-E ‖ epoch E) —
//     entropy BEFORE epoch. The first version wrote epoch before entropy.
//     Wrong field order produces a seed every other node would also have to
//     get wrong the identical way to agree with it — since nothing else
//     computes this yet, it happened to not have broken anything live, but
//     it would not have matched §4.7's own "byte-exact golden vector" test
//     requirement (Low-Level-Design M4.1) had one been written against the
//     spec instead of against the first version's own output.
//
//  2. Beacon indexing. §4.7 defines ENTROPY-E as "the beacon (sealed from
//     epoch E−1's reveals)" and its own diagram is explicit:
//     ENTROPY-9 -> entropySeed_9 -> committee_9 (i.e. ENTROPY-E seeds
//     committee E, not committee E+1). The one-epoch lag is in WHEN a value
//     becomes available (produced during E-1, finalised at the E-1/E
//     boundary), not in HOW it is indexed. The first version read
//     beacon.EpochEntropy(epoch-1) for committee epoch — an extra,
//     self-imposed offset on top of the lag already baked into §F's
//     publish timing, which would have looked up entropy one epoch too old.
//     The fix: beacon.EpochEntropy(epoch) directly — whatever Publish
//     wiring (§F, not yet built) eventually stores under key E must itself
//     store what §4.7 calls ENTROPY-E, i.e. epoch E-1's sealed VDF output.
//     That convention is §F's to satisfy, not something to compensate for
//     here with a second offset.
//
// Two things this deliberately reuses rather than reinvents, both flagged
// as open items in the design doc and repeated here so the reasoning
// travels with the code, not just the doc:
//
//   - The eligible pool is the SAME pinned/eligible validator set used for
//     block-production committees (committeeSnapshotFor) — §4.7's
//     `pinnedPool`. There is no separate "entropy revealer" registry
//     anywhere in this codebase (verified 2026-08-19) — reusing the
//     block-committee pool is the only option available today, not a
//     confirmed independent design decision.
//   - The draw itself is committee.CommitteeFor, the same seed-ranked
//     selection (A-ExpJ) block committees already use — §4.7 states this
//     explicitly ("Reuses A-ExpJ — no new selection algorithm").
//
// This function is code-complete but NON-FUNCTIONAL in the live system
// today — not a bug, a direct consequence of §F ("Publish wiring") not
// existing yet: SetBeaconSource has zero callers anywhere, so activeBeacon()
// is always nil and every call here fails closed with "no beacon source
// installed." That is correct, honest behaviour until §F lands.
import (
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/JupiterMetaLabs/avc/committee"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

// EntropyCommitteeDomain domain-separates the entropy-committee seed from
// DeriveSeed's per-height block-committee seed and from every other hash in
// this codebase. Verified byte-for-byte against Architecture §4.7's table
// ("25-byte ASCII constant").
const EntropyCommitteeDomain = "jmdt/entropy-committee/v1"

// EntropyCommitteeSize is m, Architecture §4.6's floor for the entropy-reveal
// committee.
const EntropyCommitteeSize = 13

// ErrNoBeaconInstalled is returned when SetBeaconSource has never been
// called. Deliberately distinct from committee.ErrEntropyUnavailable (which
// means "a beacon exists but hasn't published this epoch yet") — this means
// no beacon exists at all.
var ErrNoBeaconInstalled = errors.New("messaging: no beacon source installed (fail closed): call SetBeaconSource at startup")

// EntropyCommitteeSeed derives entropySeed_E per Architecture §4.7:
//
//	entropySeed_E = SHA256( domain || u64:chainID || len:ENTROPY-E || u64:epoch )
//
// entropyE MUST be what §4.7 calls ENTROPY-E — the beacon value sealed from
// epoch E-1's reveals, indexed under E by convention (see the correction
// note on this file). It is NOT epoch E's own reveal output; that is
// exactly what committee E, once selected, is being asked to go produce —
// seeding off it would be circular. The caller (SelectEntropyCommittee) is
// responsible for resolving the right value — this function has no way to
// check that itself, which is why it takes entropyE as an already-resolved
// byte slice rather than an epoch number to look up.
func EntropyCommitteeSeed(chainID, epoch uint64, entropyE []byte) committee.Seed {
	h := sha256.New()
	committee.WriteField(h, []byte(EntropyCommitteeDomain))
	committee.WriteU64(h, chainID)
	committee.WriteField(h, entropyE)
	committee.WriteU64(h, epoch)
	var out committee.Seed
	copy(out[:], h.Sum(nil))
	return out
}

// SelectEntropyCommittee picks epoch E's entropy-reveal committee.
//
// Fails closed — returns an error, never a partial, empty, or best-effort
// result — when:
//   - no BeaconSource is installed (ErrNoBeaconInstalled)
//   - ENTROPY-E was never published under key `epoch` (wraps
//     committee.ErrEntropyUnavailable) — this is also what happens for
//     whatever the network's first live epoch is, until a genesis/bootstrap
//     entropy value is published for it; no such mechanism exists yet
//     (unflagged open item — §F's design doesn't cover epoch 0's bootstrap
//     case either, only steady-state publish)
//   - the eligible pool is empty, or CommitteeFor's own validation fails
//
// On success, the returned slice always has exactly EntropyCommitteeSize
// members unless the eligible pool itself has fewer than that (CommitteeFor
// seats everyone when k >= n — the same rule block committees follow).
func SelectEntropyCommittee(epoch committee.EntropyEpoch) ([]committee.Member, error) {
	beacon := activeBeacon()
	if beacon == nil {
		return nil, ErrNoBeaconInstalled
	}

	entropy, err := beacon.EpochEntropy(epoch)
	if err != nil {
		return nil, fmt.Errorf("messaging: ENTROPY-%d unavailable (needed to seed epoch %d's entropy committee): %w", epoch, epoch, err)
	}

	snap, err := committeeSnapshotFor(uint64(epoch))
	if err != nil {
		return nil, fmt.Errorf("messaging: committee snapshot for epoch %d: %w", epoch, err)
	}

	seed := EntropyCommitteeSeed(BLS_Signer.DomainChainID(), uint64(epoch), entropy)

	members, err := committee.CommitteeFor(seed, snap, EntropyCommitteeSize)
	if err != nil {
		return nil, fmt.Errorf("messaging: entropy committee draw for epoch %d: %w", epoch, err)
	}
	return members, nil
}
