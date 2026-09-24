package Vote

// D-26(a)/D-51 cutover (AVC-CONSENSUS-HANDOVER.md, rev 7): this used to be
// Stage 2 of docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md — an additive dual-write
// into the new (avc) block-keyed vote CRDT, gated off by default alongside
// the unchanged legacy write in SubmitVote. It is no longer optional.
//
// The legacy vote-CRDT read path (Structs.processVotesFromCRDT_legacy) has
// no per-vote signature at all and keys its CRDT write on an unauthenticated
// JSON payload field (D-26(a)'s actual defect — see
// AVC/BuddyNodes/MessagePassing/Service/subscriptionService.go). A prior fix
// attempt rejected mismatched sender fields at that ingest point and was
// reverted (commit 18806fb): ListenerHandler.go legitimately republishes a
// vote it received over a direct stream to pubsub under the RELAYING node's
// own identity, so payload-sender != transport-sender on every honest relay
// too, not just on a forged one — indistinguishable at that layer.
//
// The fix that actually closes D-26(a)/(b), per the audit's own "Fix —
// design level" note, is to finish this cutover instead: decide every vote
// from the v2, block-keyed, BLS-signed keyspace (Structs.ProcessVotesFromCRDT
// -> processVotesFromCRDT_v2 -> avcvotes.TallyBlock -> verifyTallySignatures),
// which authenticates the voter by committee-registered public key and
// signature, independent of which peer relayed or synced the message to
// this node. That boundary does not care who wrote the CRDT element or how
// — only whether a valid signature from a real committee member's key backs
// it — which is why it is safe even though the CRDT-sync merge path
// (mergeVoteCRDTElement) still attributes the relaying peer as write-actor
// rather than the declared voter (that residual is D-52, an ingest-quota
// accounting gap, not a vote-forging one, and stays open/out of scope here).
//
// VoteCRDTDualWrite is therefore permanently true, not env-gated: per the
// user's standing instruction, this ships as one coordinated fleet-wide
// restart, so a runtime toggle a node could silently be missing (D-51's own
// finding: "JMDN_VOTE_CRDT_V2 makes the entire avc v2 vote keyspace inert")
// serves no purpose once every node restarts onto this build together. Kept
// as a var (not a const) only because existing tests and other packages
// still reference the symbol; do not reintroduce an env branch here.

import (
	"os"
	"strings"
)

// envOn mirrors the same helper duplicated in Security, messaging, and
// internal/reputation — it's unexported everywhere, so it's copied here
// rather than imported (no shared exported utility exists for this).
func envOn(key string, def bool) bool {
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

// VoteCRDTDualWrite used to gate the additive write into the new
// block-keyed vote CRDT via JMDN_VOTE_CRDT_V2 (default off). It is now
// permanently true — see the package doc comment above (D-26(a)/D-51
// cutover). envOn is kept below (still covered by
// TestEnvOn_DefaultsAndOverrides) but no longer determines this value.
var VoteCRDTDualWrite = true
