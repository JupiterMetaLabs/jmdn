package messaging

// Production consensus-posture lock (audit SEC-03, consensus extension).
//
// The three flags checked below are FAIL-OPEN when disabled: turning any of
// them off makes the node accept weaker/legacy consensus input (legacy
// non-block-bound votes, votes from non-committee peers, or a certified
// block whose body was never rebound to its hash). Each defaults ON (see
// consensus_hardening.go), but an operator can flip them off via env
// (JMDN_REJECT_LEGACY_VOTES=0, etc.).
//
// Disabling them may be legitimate during a mixed-version rollout, but it must
// never happen silently on a production node. This validator makes a production
// node REFUSE to boot if any of them is off, so a fail-open consensus config is
// caught at startup instead of at exploitation time.
//
// A fourth setting is checked alongside them for the same reason though it
// is not a flag: consensus.sequencer_pinned_peer_id (D-26(d)/CON-03). It has
// no on/off state — the vote-result-requester gate it feeds is always
// enforced — but leaving it unset degrades that gate's authorized set to
// committee-only, which can reject the fleet's own sequencer. See
// AuthorizedRequesterSet's doc comment (AVC/BuddyNodes/MessagePassing/
// consensus_vote_authz.go).
//
// It lives in the messaging package (not config/settings) on purpose: the flags
// are package vars here, and config/settings must not import messaging (import
// cycle — messaging already imports config/settings).

import (
	"fmt"
	"strings"

	"gossipnode/config/settings"
)

// IsProductionPosture reports whether this node should be treated as
// production: security.strict_posture is set, or network.environment is
// "mainnet". Single source of truth — main.go's SEC-03 check below and any
// other production-only gate (e.g. VDF group trust level, Sequencer/
// vdf_network_pins.go) must agree on what "production" means, or one could
// authorize what the other refuses. An unloaded config is never production —
// same reasoning as currentChainID in vdf_network_pins.go: assuming a
// default here would let an unconfigured node silently pass as testnet.
func IsProductionPosture() bool {
	// Unreachable in production: main.go calls settings.Load() at :870 and
	// InstallAVCBeaconFromEnv at :1589. The fallback exists so tests can run.
	if !settings.IsLoaded() {
		return false
	}
	cfg := settings.Get()
	return cfg.Security.StrictPosture ||
		strings.EqualFold(strings.TrimSpace(cfg.Network.Environment), "mainnet")
}

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
	// D-26(d)/CON-03: the vote-result-requester gate
	// (AVC/BuddyNodes/MessagePassing/consensus_vote_authz.go) is always
	// enforced now — there is no flag to be "off" for it. What CAN still be
	// missing is the sequencer pin it needs to authorize the sequencer
	// without degrading to a committee-only set (see config.go's doc
	// comment on SequencerPinnedPeerID). Not fail-open in the same sense as
	// the three flags above, but it can reject the fleet's own legitimate
	// sequencer's vote-result requests whenever the sequencer is not
	// independently a resolvable committee member — a liveness failure
	// worth catching at boot rather than at incident time.
	if strings.TrimSpace(settings.Get().Consensus.SequencerPinnedPeerID) == "" {
		off = append(off, "SequencerPinnedPeerID (consensus.sequencer_pinned_peer_id)")
	}
	if len(off) == 0 {
		return nil
	}
	return fmt.Errorf(
		"SEC-03 production consensus posture: refusing to start — fail-open or unpinned consensus setting(s) in production: %s — re-enable/pin each or leave production posture (clear security.strict_posture and do not run environment=mainnet)",
		strings.Join(off, "; "),
	)
}
