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
// So this file does not pin a value either. It reads both from the
// environment, validates everything it mechanically can (ValidateModulus,
// optionally ValidateChallengeShape against a published size), and — if
// anything required is missing — installs NOTHING and leaves the network
// exactly where it is today: Stage 1 (salt-based) committee selection, no
// entropy-committee selection, Finalise()/Seal() code-complete but never
// actually invoked with a live pipeline. That is a deliberate, safe
// default, not a gap to close by filling it with a placeholder number.
//
// # What to set, when ready
//
//	JMDN_AVC_VDF_MODULUS_HEX     required — the group modulus N, hex, no 0x prefix
//	JMDN_AVC_VDF_GROUP_NAME      required — a name for domain separation (e.g. "rsa-2048-frc")
//	JMDN_AVC_VDF_DIFFICULTY_T    required — positive uint64, the PINNED squaring count
//	                             from an offline vdf.Calibrate run on the slowest
//	                             fleet hardware — never computed here at startup,
//	                             per vdf.go's own "never call it at runtime" warning
//	JMDN_AVC_VDF_MODULUS_BITS    optional — expected bit length; checked via
//	JMDN_AVC_VDF_MODULUS_DIGITS  optional — expected decimal digits; both must be set
//	                             together to enable vdf.ValidateChallengeShape
//	JMDN_AVC_BEACON_RETAIN_EPOCHS optional — default committee.MinRetainedEpochs (3)
//
// Even with all of these set, ValidateModulus/ValidateChallengeShape are
// mechanical checks only — they cannot confirm nobody else knows N's
// factorisation. That provenance guarantee has to come from how N was
// obtained (an RSA Factoring Challenge modulus from a primary source, or a
// multi-party ceremony) — an operational decision this code cannot verify.
import (
	"errors"
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

	group, err := vdf.NewRSAGroup(n, groupName)
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

	messaging.SetBeaconSource(sink)
	SetVDFPipeline(pipeline)
	InstallEpochFinalisedHook()

	log.Warn().Str("group", groupName).Uint64("difficulty_t", difficulty).Uint64("retain_epochs", retain).
		Msg("entropy: AVC beacon (Stage 2 RANDAO+VDF) INSTALLED — committee selection now depends on genuine RANDAO+VDF entropy once published; the genesis/bootstrap gap documented in messaging.SelectEntropyCommittee still applies until a first entropy value exists for the network's earliest epoch")

	return true, nil
}
