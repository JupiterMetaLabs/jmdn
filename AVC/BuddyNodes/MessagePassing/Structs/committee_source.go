package Structs

import (
	"fmt"
	"os"
	"strings"
)

// authorizedCommitteeFn is the injected committee source for the vote-CRDT
// read path (Stage 3.5 of docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md). Injected
// rather than imported: messaging -> Vote -> MessagePassing -> Structs
// already exists (messaging/broadcast.go imports Vote; Vote/Trigger.go
// imports MessagePassing; MessagePassing/ListenerHandler.go imports
// Structs), so Structs -> messaging would be an import cycle. This seam
// originally lived in package MessagePassing itself, but Stage 4's
// ProcessVotesFromCRDT (the actual caller) lives in package Structs, and
// MessagePassing -> Structs already exists, so Structs -> MessagePassing
// would ALSO cycle. Structs has no back-edge to either package, so the seam
// lives here. Same pattern as SetSlotStoreReadyFn in
// MessagePassing/consensus_sync_gate.go — read that function's doc comment
// if this one is unclear.
var authorizedCommitteeFn func() (map[string]string, error)

// SetAuthorizedCommitteeFn wires the committee source. Call once at startup,
// beside the existing MessagePassing.SetSlotStoreReadyFn wiring in main.go.
func SetAuthorizedCommitteeFn(fn func() (map[string]string, error)) {
	authorizedCommitteeFn = fn
}

// authorizedCommittee returns the injected peerID -> lowercase-hex-BLS-pubkey
// map, or an error. FAIL CLOSED: an unset source is a hard error, never an
// empty map — TallyBlock treats an empty map as "authorize nobody," which is
// a legitimate (if unlikely) real state; conflating it with "the source was
// never installed" would hide a startup-wiring bug behind a state that looks
// like normal operation.
func authorizedCommittee() (map[string]string, error) {
	if authorizedCommitteeFn == nil {
		return nil, fmt.Errorf("Structs: authorized-committee source not installed (fail closed)")
	}
	return authorizedCommitteeFn()
}

// authorizedCommitteeForHeightFn, when installed, resolves the authorized set
// for the block being tallied (W1: the chain-anchored pool of that height's
// selection period, messaging.AuthorizedCommitteeForTallyAtHeight). Unset, the
// tally keeps the height-less source above. Same injection pattern and reason.
var authorizedCommitteeForHeightFn func(height uint64) (map[string]string, error)

// SetAuthorizedCommitteeForHeightFn wires the per-height committee source.
func SetAuthorizedCommitteeForHeightFn(fn func(height uint64) (map[string]string, error)) {
	authorizedCommitteeForHeightFn = fn
}

// authorizedCommitteeFor resolves the authorized set for height, preferring the
// per-height source when installed. Same fail-closed contract.
func authorizedCommitteeFor(height uint64) (map[string]string, error) {
	if authorizedCommitteeForHeightFn != nil {
		return authorizedCommitteeForHeightFn(height)
	}
	return authorizedCommittee()
}

// authorizedVotersForHeightFn is the injected VOTER-set source — who a buddy
// COUNTS votes from — as distinct from authorizedCommitteeForHeightFn, who
// may SIGN a buddy result. "Count every validator's vote in the buddy tally
// again" LLD, C6. Wired at startup via SetAuthorizedVotersForHeightFn, beside
// the committee seam above.
var authorizedVotersForHeightFn func(height uint64) (map[string]string, error)

// SetAuthorizedVotersForHeightFn wires the per-height voter-set source.
func SetAuthorizedVotersForHeightFn(fn func(height uint64) (map[string]string, error)) {
	authorizedVotersForHeightFn = fn
}

// ValidatorVoterSetEnabled is the rollback switch for this change.
// JMDN_VALIDATOR_VOTER_SET=0 restores the pre-change behaviour: the tally
// authorizes against the committee set exactly as before, byte-identical.
var ValidatorVoterSetEnabled = envOnCommitteeSource("JMDN_VALIDATOR_VOTER_SET", true)

func envOnCommitteeSource(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// authorizedVotersFor resolves the set of peers whose votes a buddy COUNTS at
// tally time. With the flag off, or no voter-set source installed, this is
// byte-identical to authorizedCommitteeFor — a complete rollback requiring no
// other action. With the flag on, it is the uncapped eligible validator pool
// (or the chain-anchored pool for that height), not the capped committee —
// see messaging.AuthorizedVotersForTallyAtHeight's doc comment for why.
func authorizedVotersFor(height uint64) (map[string]string, error) {
	if !ValidatorVoterSetEnabled || authorizedVotersForHeightFn == nil {
		return authorizedCommitteeFor(height)
	}
	return authorizedVotersForHeightFn(height)
}
