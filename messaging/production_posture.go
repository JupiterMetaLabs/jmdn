package messaging

// Production consensus-posture lock (audit SEC-03, consensus extension).
//
// The three flags below are FAIL-OPEN when disabled: turning any of them off
// makes the node accept weaker/legacy consensus input (legacy non-block-bound
// votes, votes from non-committee peers, or a certified block whose body was
// never rebound to its hash). Each defaults ON (see consensus_hardening.go),
// but an operator can flip them off via env (JMDN_REJECT_LEGACY_VOTES=0, etc.).
//
// Disabling them may be legitimate during a mixed-version rollout, but it must
// never happen silently on a production node. This validator makes a production
// node REFUSE to boot if any of them is off, so a fail-open consensus config is
// caught at startup instead of at exploitation time.
//
// It lives in the messaging package (not config/settings) on purpose: the flags
// are package vars here, and config/settings must not import messaging (import
// cycle — messaging already imports config/settings).

import (
	"fmt"
	"strings"
)

// ValidateProductionConsensusPosture returns a non-nil (fatal) error when the
// node is in a production posture AND any fail-open consensus hardening flag is
// disabled. In a non-production posture it returns nil (the flags may be flipped
// for testnet / mixed-version rollout).
//
// The caller decides what "production" means and passes it in; today main.go
// treats a node as production when security.strict_posture is set OR
// network.environment == "mainnet". Callers should os.Exit / fatal on a non-nil
// return — this is fail-closed by design.
func ValidateProductionConsensusPosture(production bool) error {
	if !production {
		return nil
	}
	var off []string
	if !RejectLegacyVotes {
		off = append(off, "RejectLegacyVotes (JMDN_REJECT_LEGACY_VOTES)")
	}
	if !EnforceCommitteeRegistry {
		off = append(off, "EnforceCommitteeRegistry (JMDN_ENFORCE_COMMITTEE_REGISTRY)")
	}
	if !EnforceBodyBinding {
		off = append(off, "EnforceBodyBinding (JMDN_ENFORCE_BODY_BINDING)")
	}
	if len(off) == 0 {
		return nil
	}
	return fmt.Errorf(
		"SEC-03 production consensus posture: refusing to start — fail-open consensus flag(s) disabled in production: %s — re-enable each (set the env var to 1/unset it) or leave production posture (clear security.strict_posture and do not run environment=mainnet)",
		strings.Join(off, "; "),
	)
}
