package MessagePassing

import "fmt"

// authorizedCommitteeFn is the injected committee source for the vote-CRDT
// read path (Stage 3.5 of docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md). Injected
// rather than imported: messaging -> Vote -> MessagePassing already exists
// (messaging/broadcast.go imports Vote; Vote/Trigger.go imports this
// package), so MessagePassing -> messaging would be an import cycle. Same
// pattern as SetSlotStoreReadyFn in consensus_sync_gate.go — read that
// function's doc comment if this one is unclear.
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
		return nil, fmt.Errorf("MessagePassing: authorized-committee source not installed (fail closed)")
	}
	return authorizedCommitteeFn()
}
