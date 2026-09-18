package selection

import (
	"context"
	"github.com/JupiterMetaLabs/ion"
	"time"
)

// FilterConfig defines filtering rules
type FilterConfig struct {
	MinReputationScore float64
	MinSelectionScore  float64 // Minimum selection score (0.5 <= score < 1.0)
	MaxSelectionScore  float64 // Maximum selection score (exclude score >= 1.0)
	NodeTimeoutMinutes int
	NodesPerRegion     int // Target nodes per region for geographic diversity
}

// DefaultFilterConfig returns sensible defaults.
//
// Band rationale (D-63): the eligibility interval is [MinSelectionScore,
// MaxSelectionScore) = [0.50, 0.95).
//
//   - Floor 0.50: a reputation weight below this drops a peer from selection.
//     The reputation→weight remap (internal/reputation/selection_weight.go) is
//     weight = 0.20 + repScore for repScore in [0.10,0.50], so weight 0.50 ⇔
//     repScore 0.30. Decay pulls repScore toward Start (0.50) at 0.9^epochs
//     (EpochSeconds=3600), so a peer recovers ABOVE the floor without a restart:
//     a single BadSignature (repScore 0.20) heals in ~3.85 epochs (~3.85 h) and
//     an Equivocation (0.10) in ~6.58 epochs — provided no NEW faults land. The
//     2026-09-17 halt was NOT a decay problem; it was the feedback loop adding
//     faults faster than decay healed, which D-59/D-60/D-61 break.
//   - Ceiling 0.95 ("reserved/system node"): UNREACHABLE from reputation —
//     selection_weight.go caps the healthy half at healthyCeil = 0.94 < 0.95, so
//     no reputation-derived weight ever hits this branch. It fires only if
//     peer.Weights is set >= 0.95 by another path (e.g. an operator hand-setting
//     1.0 as a "trusted" marker), which would then EXCLUDE that node — a
//     foot-gun. Kept for that explicit-override semantics, not for reputation.
//
// The D-59 fail-safe (FilterEligibleOrActive) guarantees neither bound can, on
// its own, empty the candidate set and halt the chain.
func DefaultFilterConfig() FilterConfig {
	return FilterConfig{
		MinReputationScore: 0.0,  // Accept all reputation scores (separate optional filter)
		MinSelectionScore:  0.5,  // Only nodes with weight >= 0.5 (⇔ repScore >= 0.30)
		MaxSelectionScore:  0.95, // Exclude weight >= 0.95 (reserved/system; unreachable from reputation, healthyCeil=0.94)
		NodeTimeoutMinutes: 10,
		NodesPerRegion:     2, // Try to get 2 nodes per region
	}
}

// FilterEligible applies filtering rules to node list
func FilterEligible(myNodeID string, nodes []Node, config FilterConfig) []Node {
	if len(nodes) == 0 {
		return nodes
	}

	// Pre-allocate with reasonable capacity
	eligible := make([]Node, 0, len(nodes)/2)

	// Calculate timeout threshold once
	// now := time.Now().UTC()
	// timeoutDuration := time.Duration(config.NodeTimeoutMinutes) * time.Minute

	for i := range nodes {
		node := &nodes[i]

		// Skip self
		if node.PeerId == myNodeID {
			continue // self is not a filter-drop worth logging
		}

		// Check if node is active
		if !node.IsActive {
			logger().Warn(context.Background(), "selection: skipping node — inactive",
				ion.String("peer_id", node.PeerId))
			continue
		}

		// Selection-score floor (reputation weight below the eligibility band).
		if node.SelectionScore < config.MinSelectionScore {
			logger().Warn(context.Background(), "selection: skipping node — selection score below floor",
				ion.String("peer_id", node.PeerId),
				ion.Float64("selection_score", node.SelectionScore),
				ion.Float64("min_selection_score", config.MinSelectionScore))
			continue
		}

		// Selection-score ceiling (reserved/system-node territory).
		if node.SelectionScore >= config.MaxSelectionScore {
			logger().Warn(context.Background(), "selection: skipping node — selection score at/above ceiling (reserved/system node)",
				ion.String("peer_id", node.PeerId),
				ion.Float64("selection_score", node.SelectionScore),
				ion.Float64("max_selection_score", config.MaxSelectionScore))
			continue
		}

		// Optional reputation-score floor.
		if config.MinReputationScore > 0 && node.ReputationScore < config.MinReputationScore {
			logger().Warn(context.Background(), "selection: skipping node — reputation score below floor",
				ion.String("peer_id", node.PeerId),
				ion.Float64("reputation_score", node.ReputationScore),
				ion.Float64("min_reputation_score", config.MinReputationScore))
			continue
		}

		// // Check if node is online (within timeout window) — disabled (see GetFilterStats).
		// if now.Sub(node.LastSeen) > timeoutDuration { ... }

		// Node passed all filters
		eligible = append(eligible, *node)
	}

	return eligible
}

// activeNonSelfNodes returns every node that is active and not self, WITHOUT
// applying the reputation/selection-score band. It is the fail-safe candidate
// set: reputation is a future-selection signal that must never, on its own,
// starve the committee to empty and halt block production (D-59). A node with
// no usable p2p presence (inactive / self) is still excluded, because selecting
// it cannot yield a working buddy.
func activeNonSelfNodes(myNodeID string, nodes []Node) []Node {
	out := make([]Node, 0, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		if n.PeerId == myNodeID || !n.IsActive {
			continue
		}
		out = append(out, *n)
	}
	return out
}

// FilterEligibleOrActive applies the normal band filter, but if that empties
// the candidate set while active non-self peers DO exist, it falls back to the
// unbanded active set and logs loudly. This converts "reputation collapsed the
// whole fleet below the floor" from a chain halt (ErrNoPeersAvailable) into a
// degraded-but-live round. Returns (candidates, usedFallback).
//
// Rationale: the reputation→selection-weight pipeline is observe-only by design
// (internal/reputation package doc); it must not be able to halt liveness. The
// incident of 2026-09-17 (29 peers pushed to weights 0.30–0.43 → 0 candidates →
// ErrNoPeersAvailable → >1h halt) is exactly this path.
func FilterEligibleOrActive(myNodeID string, nodes []Node, config FilterConfig) ([]Node, bool) {
	eligible := FilterEligible(myNodeID, nodes, config)
	if len(eligible) > 0 {
		return eligible, false
	}
	fallback := activeNonSelfNodes(myNodeID, nodes)
	if len(fallback) == 0 {
		return eligible, false // genuinely no peers; caller returns ErrNoPeersAvailable
	}
	logger().Warn(context.Background(),
		"selection: FAIL-SAFE — reputation/selection band emptied the candidate set; "+
			"falling back to the unbanded active set to keep the chain live "+
			"(reputation is observe-only and must not halt liveness)",
		ion.Int("active_nonself", len(fallback)),
		ion.Int("total_nodes", len(nodes)))
	return fallback, true
}

// GroupNodesByRegion groups nodes by their region
func GroupNodesByRegion(nodes []Node) map[string][]Node {
	regionMap := make(map[string][]Node)

	for _, node := range nodes {
		region := node.Region
		if region == "" || region == "UNKNOWN" {
			region = "REGION-UNKNOWN"
		}
		regionMap[region] = append(regionMap[region], node)
	}

	return regionMap
}

// GetEligibleCount returns count of nodes that would pass filters
func GetEligibleCount(myNodeID string, nodes []Node, config FilterConfig) int {
	eligible := FilterEligible(myNodeID, nodes, config)
	return len(eligible)
}

// FilterStats provides statistics about filtering
type FilterStats struct {
	TotalNodes         int
	FilteredSelf       int
	FilteredInactive   int
	FilteredLowScore   int
	FilteredHighScore  int
	FilteredReputation int
	FilteredTimeout    int
	EligibleNodes      int
	RegionCount        int // Number of unique regions
}

// GetFilterStats returns detailed filtering statistics
func GetFilterStats(myNodeID string, nodes []Node, config FilterConfig) FilterStats {
	stats := FilterStats{
		TotalNodes: len(nodes),
	}

	now := time.Now().UTC()
	timeoutDuration := time.Duration(config.NodeTimeoutMinutes) * time.Minute

	for i := range nodes {
		node := &nodes[i]

		if node.PeerId == myNodeID {
			stats.FilteredSelf++
			continue
		}

		if !node.IsActive {
			stats.FilteredInactive++
			continue
		}

		if node.SelectionScore < config.MinSelectionScore {
			stats.FilteredLowScore++
			continue
		}

		if node.SelectionScore >= config.MaxSelectionScore {
			stats.FilteredHighScore++
			continue
		}

		if config.MinReputationScore > 0 && node.ReputationScore < config.MinReputationScore {
			stats.FilteredReputation++
			continue
		}

		if now.Sub(node.LastSeen) > timeoutDuration {
			stats.FilteredTimeout++
			continue
		}

		stats.EligibleNodes++
	}

	// Calculate region diversity counts
	regionSet := make(map[string]bool)

	for _, node := range nodes {
		// Count regions
		region := node.Region
		if region == "" || region == "UNKNOWN" {
			region = "REGION-UNKNOWN"
		}
		regionSet[region] = true
	}

	stats.RegionCount = len(regionSet)

	return stats
}
