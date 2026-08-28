package Structs

import "fmt"

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
