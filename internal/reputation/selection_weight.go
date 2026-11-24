// Phase A4.3 / Decision A4-1 (docs/A4-REPUTATION-WEIGHTING-PLAN.md).
//
// PROBLEM: reputation's native range is [Floor, Cap] = [0.10, 1.00], neutral
// at Start = 0.50. Buddy selection's eligibility band (AVC/NodeSelection/pkg
// /selection/filter.go DefaultFilterConfig) is the half-open interval
// [MinSelectionScore, MaxSelectionScore) = [0.50, 0.95). Writing a raw
// reputation score straight into peer.Weights (and from there into
// selection.Node.SelectionScore -- see seednode.go convertPeerRecordToNode /
// convertBuddyPeerRecordToNode, which use peer.Weights verbatim when > 0) has
// two edge defects:
//
//  1. A peer that has NEVER been observed sits at exactly Start = 0.50 -- the
//     exact eligibility floor. One Absent event (-0.10 -> 0.40) then drops it
//     out of the committee entirely on a single missed vote, which is a
//     liveness blip, not a proven fault.
//  2. A peer at the reputation Cap = 1.00 maps to exactly 0.95 -> hits
//     MaxSelectionScore's exclusive edge and *also* becomes ineligible
//     (filter.go treats score >= 0.95 as reserved/system-node territory).
//
// SOLUTION: SelectionWeight is a two-segment, continuous, monotonic remap
// anchored at Start:
//
//	repScore in [Start, Cap] (the "healthy" half, non-negative history)
//	    -> linearly onto [healthyFloor, healthyCeil] = [0.70, 0.94]
//	repScore in [Floor, Start) (the "faulted" half, some objective fault)
//	    -> linearly onto [faultedFloor, healthyFloor) = [0.30, 0.70)
//
// The two segments meet exactly at Start -> 0.70, so the map has no
// discontinuity. Concretely, from a never-observed peer (Start = 0.50):
//
//	0 faults                              -> 0.70  (safely mid-band)
//	1 Absent   (0.50 -> 0.40)             -> 0.60  (still eligible)
//	2 Absent   (0.50 -> 0.30)             -> ~0.50 (right at the edge -- float
//	                                                rounding can tip this exact
//	                                                case either way; treat it as
//	                                                the practical cliff, not a
//	                                                guaranteed-eligible line)
//	3 Absent   (0.50 -> 0.20)             -> 0.40  (ineligible)
//	1 BadSignature (0.50 -> 0.20)         -> 0.40  (ineligible)
//	1 Equivocation (0.50 -> Floor 0.10)   -> 0.30  (ineligible)
//
// So a single liveness miss no longer excludes a fresh peer, while a real
// protocol fault (bad signature, and certainly equivocation, which always
// bottoms out at Floor in one event -- see Delta) crosses back below
// MinSelectionScore and the peer drops out of the next ListBuddy selection.
// A perfect-history peer (Cap = 1.00) maps to 0.94, safely inside the band
// and clear of the 0.95 exclusive ceiling.
//
// This is a pure, reversible policy choice (Assumed, per the A4 plan doc's
// three options -- linear remap was chosen over lowering MinSelectionScore or
// accepting the raw range as-is): only these four constants need to change to
// retune it, and every caller goes through this one function.
package reputation

const (
	// healthyFloor/healthyCeil bound where the "healthy" half of the
	// reputation range (repScore >= Start) lands in selection-score space.
	healthyFloor = 0.70
	healthyCeil  = 0.94
	// faultedFloor bounds where the "faulted" half (repScore < Start) lands;
	// its ceiling is healthyFloor, so the map is continuous at Start.
	faultedFloor = 0.30
)

// SelectionWeight remaps a raw reputation score (as returned by Store.Score /
// Store.Snapshot, range [Floor, Cap]) onto the value that should be written to
// the seed's peer.Weights field, so that downstream selection-score derivation
// (seednode.go's convertPeerRecordToNode / convertBuddyPeerRecordToNode) sees
// a value that behaves sanely against filter.go's eligibility band. See the
// package-level doc comment above for the exact mapping and worked examples.
//
// Input is clamped to [Floor, Cap] first, so a caller passing an already-valid
// Store score never needs to pre-clamp.
func SelectionWeight(repScore float64) float64 {
	repScore = clamp(repScore)
	if repScore >= Start {
		frac := (repScore - Start) / (Cap - Start) // 0..1, Cap>Start always holds
		return healthyFloor + frac*(healthyCeil-healthyFloor)
	}
	frac := (repScore - Floor) / (Start - Floor) // 0..1, Start>Floor always holds
	return faultedFloor + frac*(healthyFloor-faultedFloor)
}

// SnapshotSelectionWeights returns every observed peer's CURRENT reputation
// score already remapped via SelectionWeight -- the map Phase A4.2's seed push
// should send as peer.Weights. Convenience wrapper over Default.Snapshot() so
// call sites never remap by hand and risk skipping the clamp/anchor logic.
func SnapshotSelectionWeights() map[string]float64 {
	raw := Default.Snapshot()
	out := make(map[string]float64, len(raw))
	for peerID, score := range raw {
		out[peerID] = SelectionWeight(score)
	}
	return out
}
