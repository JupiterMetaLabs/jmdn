package Vote

// Stage 2 of docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md: dual-write into the new
// (avc) block-keyed vote CRDT, alongside the unchanged legacy write in
// SubmitVote. Flag off by default — same pattern as JMDN_M2B_HASH,
// JMDN_COMMITTEE_V2, JMDN_TIMEOUT_CERT_WIRING.

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

// VoteCRDTDualWrite gates the additive write into the new block-keyed vote
// CRDT. When off, SubmitVote's behavior is byte-identical to before this
// stage — no extra BLS computation, no extra CRDT write, nothing.
var VoteCRDTDualWrite = envOn("JMDN_VOTE_CRDT_V2", false)
