package Sequencer

// Stage F of the M4 pipeline (AVC-M4-Entropy-Reveal-Pipeline-Design.md §F) —
// constructs the network's real *beacon.Pipeline and installs it (Stage E's
// SetVDFPipeline, messaging's SetBeaconSource, and the Stage-D->E hook), so
// entropy actually gets sealed and published instead of only being wired.
//
// # Why this reads two parameters from the environment instead of supplying them
//
// A live Pipeline needs a VDF group (avc/vdf.Group — in practice an RSA
// modulus of UNKNOWN factorisation) and a difficulty T (the sequential-
// squaring count calibrated to the target VDF delay, ~1200-1410s per
// VDF-Implementation-Handoff.md §0). Both are explicitly, repeatedly flagged
// in the underlying packages as values that must NOT be invented by
// whoever is wiring the code up:
//
//   - avc/vdf.go's own package doc: "Do NOT generate [N] yourself with
//     crypto/rsa. You would hold the trapdoor... This package cannot stop
//     you doing it and will not detect it" — and NewRSAGroup's doc comment
//     names, specifically, an earlier real incident in this exact repo
//     where a fabricated/misremembered "RSA-2048" value was hard-coded and
//     was wrong (753 digits reproduced from memory, where RSA-2048 is 617)
//     while still passing every mechanical check ValidateModulus can run.
//   - VDF-Implementation-Handoff.md §0: "T is derived from T_vdf via
//     vdf.Calibrate, run on the slowest hardware in the actual validator
//     fleet — UNVERIFIED: this session does not know the fleet's slowest
//     node spec... do not guess a number."
//
// So this file does not pin a VALUE. It reads both from the environment,
// validates everything it mechanically can, and — if anything required is
// missing — installs NOTHING and leaves the network exactly where it is
// today: Stage 1 (salt-based) committee selection, no entropy-committee
// selection, Finalise()/Seal() code-complete but never actually invoked with
// a live pipeline. That is a deliberate, safe default, not a gap to close by
// filling it with a placeholder number.
//
// What it DOES pin, since 2026-08-25, is which values are acceptable. The
// modulus now goes through vdf.NewPinnedRSAGroup (see buildVDFGroup below),
// which requires the group name to match a provenance record in avc/vdf's
// registry and the modulus to match that record's verified digest. Supplying
// an unpinned modulus is refused unless the operator explicitly opts out.
// This does not — cannot — verify that nobody knows N's factorisation; it
// closes the realistic failure, which is the wrong number being supplied by
// convenience or by accident.
//
// # What to set, when ready
//
//	JMDN_AVC_VDF_MODULUS_HEX     required — the group modulus N, hex, no 0x prefix
//	JMDN_AVC_VDF_GROUP_NAME      required — MUST name an entry in avc/vdf's pinned
//	                             registry (e.g. "rsa-2048-frc"); run
//	                             `go run ./cmd/vdfpin -list` in the avc repo to see it
//	JMDN_AVC_VDF_DIFFICULTY_T    required — positive uint64, the PINNED squaring count
//	                             from an offline vdf.Calibrate run on the slowest
//	                             fleet hardware — never computed here at startup,
//	                             per vdf.go's own "never call it at runtime" warning
//	JMDN_AVC_VDF_MODULUS_BITS    optional — expected bit length; checked via
//	JMDN_AVC_VDF_MODULUS_DIGITS  optional — expected decimal digits; both must be set
//	                             together to enable vdf.ValidateChallengeShape.
//	                             Redundant once the modulus is pinned, since the
//	                             registry record carries its own dimensions and
//	                             NewPinnedRSAGroup enforces them unconditionally
//	JMDN_AVC_VDF_ALLOW_UNPINNED_MODULUS
//	                             optional — proceed with a modulus that has no pinned
//	                             provenance. Logs a security finding on EVERY startup.
//	                             For local and testnet use, or a ceremony output not
//	                             yet added to the registry. Never set on mainnet
//	JMDN_AVC_BEACON_RETAIN_EPOCHS optional — default committee.MinRetainedEpochs (3)
//
// The provenance guarantee itself still has to come from how N was obtained
// (an RSA Factoring Challenge modulus diffed against a primary source, or a
// multi-party ceremony). What changed is that this code now refuses to run
// until somebody has recorded that they did it.
import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/JupiterMetaLabs/avc/beacon"
	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/vdf"
	"github.com/rs/zerolog/log"

	"gossipnode/messaging"
)

// allowUnpinnedModulusEnv is the deliberate, loud escape hatch from pinned
// provenance. It exists for two legitimate cases - a local/testnet modulus,
// and the output of a future multi-party ceremony that has not yet been
// added to avc/vdf's registry - and for nothing else.
//
// It is a separate variable rather than a value of the existing ones so that
// running unpinned is always an explicit act. An operator cannot reach it by
// misconfiguring a modulus, and a developer cannot reach it by copying a
// working local setup into production without noticing what they copied.
const allowUnpinnedModulusEnv = "JMDN_AVC_VDF_ALLOW_UNPINNED_MODULUS"

// buildVDFGroup constructs the VDF group, preferring pinned provenance.
//
// Default path: vdf.NewPinnedRSAGroup, which requires the group name to name
// a registry entry, the modulus to match that entry's published dimensions,
// AND the modulus to match the entry's pinned digest. A wrong-but-plausible
// modulus passes ValidateModulus and can be made to pass the shape check;
// only the digest distinguishes the number somebody actually verified
// against a primary source from one that merely looks right. This repository
// has already shipped such a value once - see avc/vdf.NewRSAGroup's doc
// comment - which is why the strict path is the default rather than an
// option.
//
// Escape hatch: with JMDN_AVC_VDF_ALLOW_UNPINNED_MODULUS set, falls back to
// the unpinned vdf.NewRSAGroup and logs an error on EVERY startup naming
// exactly what is unverified. Not a warning: a node running consensus
// entropy on an unverified modulus is a standing security finding, and the
// log line should read like one every time somebody looks.
//
// Neither path can verify that nobody knows the modulus's factorisation.
// That is not a gap in this function - it is not decidable from N, by
// anything, ever. What pinning guarantees is narrower and achievable: that
// the modulus in use is the exact number the team deliberately chose.
func buildVDFGroup(n *big.Int, groupName string) (vdf.Group, error) {
	group, pinnedErr := vdf.NewPinnedRSAGroup(n, groupName)
	if pinnedErr == nil {
		log.Info().Str("group", groupName).
			Msg("entropy: VDF modulus matches its pinned provenance digest")
		return group, nil
	}

	if strings.TrimSpace(os.Getenv(allowUnpinnedModulusEnv)) == "" {
		return nil, fmt.Errorf("entropy: refusing to install the AVC beacon with an unpinned VDF modulus: %w\n\n"+
			"If this is a local or testnet modulus, or a ceremony output not yet added to "+
			"avc/vdf's registry, set %s=1 to proceed - it will log a security finding on "+
			"every startup. Do not set it on mainnet",
			pinnedErr, allowUnpinnedModulusEnv)
	}

	// The override waives the DIGEST, not every provenance check. If the name
	// is one the registry knows, its published dimensions are still known and
	// still enforced here — vdf.NewRSAGroup below runs ValidateModulus only,
	// so without this a modulus of visibly the wrong size would be accepted
	// under a name that documents exactly what size it should be.
	if rec, known := vdf.LookupProvenance(groupName); known {
		if shapeErr := vdf.ValidateChallengeShape(n, rec.Bits, rec.Digits); shapeErr != nil {
			return nil, fmt.Errorf("entropy: %s is set, but the modulus does not have the "+
				"published dimensions of %q (%d bits, %d digits): %w. The override waives the "+
				"pinned-digest requirement, not the shape of a modulus whose name is known",
				allowUnpinnedModulusEnv, rec.Name, rec.Bits, rec.Digits, shapeErr)
		}
	}

	group, err := vdf.NewRSAGroup(n, groupName)
	if err != nil {
		return nil, err
	}

	digest, _ := vdf.ModulusDigest(n)
	log.Error().
		Str("group", groupName).
		Str("modulus_sha256", digest).
		Int("modulus_bits", n.BitLen()).
		Str("override", allowUnpinnedModulusEnv).
		Str("refused_because", pinnedErr.Error()).
		Msg("entropy: SECURITY - running with an UNPINNED VDF modulus. Nobody has verified " +
			"this number against a primary source, so it cannot be distinguished from a " +
			"wrong-but-plausible value, and if its factorisation is known to anyone they can " +
			"evaluate the VDF instantly and steer committee selection. Acceptable on a " +
			"testnet; never on mainnet")

	return group, nil
}

// InstallAVCBeaconFromEnv builds and installs the Stage-2 beacon pipeline
// from environment configuration, if and only if every required value is
// present and passes validation.
//
// installed=false, err=nil is the expected, common case: not configured
// yet, nothing changes, the node stays on Stage 1. err is returned only
// when a value IS present but fails validation (bad hex; a modulus that is
// even, prime, too small, or has a small factor; a zero difficulty) —
// deliberately loud, since silently ignoring a bad-but-present value would
// be far worse than refusing to start Stage 2.
//
// Call once at node startup, after settings.Load() and before the
// consensus loop starts.
func InstallAVCBeaconFromEnv() (installed bool, err error) {
	modulusHex := strings.TrimSpace(os.Getenv("JMDN_AVC_VDF_MODULUS_HEX"))
	groupName := strings.TrimSpace(os.Getenv("JMDN_AVC_VDF_GROUP_NAME"))
	difficultyStr := strings.TrimSpace(os.Getenv("JMDN_AVC_VDF_DIFFICULTY_T"))

	if modulusHex == "" || groupName == "" || difficultyStr == "" {
		log.Info().Msg("entropy: AVC beacon (Stage 2 RANDAO+VDF) not configured — " +
			"JMDN_AVC_VDF_MODULUS_HEX / JMDN_AVC_VDF_GROUP_NAME / JMDN_AVC_VDF_DIFFICULTY_T " +
			"not all set; staying on Stage 1 (salt-based) committee selection")
		return false, nil
	}

	n, ok := new(big.Int).SetString(modulusHex, 16)
	if !ok {
		return false, errors.New("entropy: JMDN_AVC_VDF_MODULUS_HEX is not valid hexadecimal")
	}

	bitsStr := strings.TrimSpace(os.Getenv("JMDN_AVC_VDF_MODULUS_BITS"))
	digitsStr := strings.TrimSpace(os.Getenv("JMDN_AVC_VDF_MODULUS_DIGITS"))
	if bitsStr != "" && digitsStr != "" {
		bits, e1 := strconv.Atoi(bitsStr)
		digits, e2 := strconv.Atoi(digitsStr)
		if e1 != nil || e2 != nil {
			return false, errors.New("entropy: JMDN_AVC_VDF_MODULUS_BITS/DIGITS must be integers")
		}
		if shapeErr := vdf.ValidateChallengeShape(n, bits, digits); shapeErr != nil {
			return false, shapeErr
		}
	}

	group, err := buildVDFGroup(n, groupName)
	if err != nil {
		return false, err
	}

	difficulty, err := strconv.ParseUint(difficultyStr, 10, 64)
	if err != nil || difficulty == 0 {
		return false, errors.New("entropy: JMDN_AVC_VDF_DIFFICULTY_T must be a positive uint64")
	}

	retain := uint64(committee.MinRetainedEpochs)
	if retainStr := strings.TrimSpace(os.Getenv("JMDN_AVC_BEACON_RETAIN_EPOCHS")); retainStr != "" {
		r, rErr := strconv.ParseUint(retainStr, 10, 64)
		if rErr != nil {
			return false, errors.New("entropy: JMDN_AVC_BEACON_RETAIN_EPOCHS must be a positive uint64")
		}
		retain = r
	}

	sink, err := committee.NewBeaconSource(retain)
	if err != nil {
		return false, err
	}

	pipeline, err := beacon.New(group, difficulty, sink)
	if err != nil {
		return false, err
	}

	// Restore entropy this node finalised before its last restart, BEFORE the
	// sink is published to the rest of the process.
	//
	// A failure here is FATAL to installation rather than a warning: the only
	// way it fails is a conflicting durable value, which means this node once
	// accepted an entropy value that disagrees with what it holds now. Seating
	// committees from that is worse than not starting Stage 2.
	if restored, rerr := messaging.RehydrateBeaconFromDisk(sink); rerr != nil {
		return false, fmt.Errorf("entropy: refusing to install the AVC beacon — restoring persisted "+
			"epoch entropy failed after %d epoch(s): %w", restored, rerr)
	}

	messaging.SetBeaconSource(sink)
	SetVDFPipeline(pipeline)
	InstallEpochFinalisedHook()
	// Receive side. Installed with the pipeline it depends on, so a node can
	// never end up able to SEAL but not to ADOPT — the asymmetry that made
	// every non-proposing node pay a full local evaluation.
	InstallVDFProofAcceptor()

	log.Warn().Str("group", groupName).Uint64("difficulty_t", difficulty).Uint64("retain_epochs", retain).
		Msg("entropy: AVC beacon (Stage 2 RANDAO+VDF) INSTALLED — committee selection now depends on genuine RANDAO+VDF entropy once published; the genesis/bootstrap gap documented in messaging.SelectEntropyCommittee still applies until a first entropy value exists for the network's earliest epoch")

	return true, nil
}
