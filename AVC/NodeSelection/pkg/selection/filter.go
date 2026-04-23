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

// DefaultFilterConfig returns sensible defaults
func DefaultFilterConfig() FilterConfig {
	return FilterConfig{
		MinReputationScore: 0.0,  // Accept all reputation scores (or use 0.5 if you want)
		MinSelectionScore:  0.5,  // Only nodes with score >= 0.5
		MaxSelectionScore:  0.95, // Exclude nodes with score >= 1.0 (perfect score = reserved/system nodes)
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
			logger().Warn(context.Background(), "Skipping node due to timeout", ion.Int("timeout", -1))
			continue
		}

		// // Check if node is active
		if !node.IsActive {
			logger().Warn(context.Background(), "Skipping node due to timeout", ion.Int("timeout", 0))
			continue
		}

		// Check selection score (CRITICAL: 0.5 <= score < 1.0)
		if node.SelectionScore < config.MinSelectionScore {
			logger().Warn(context.Background(), "Skipping node due to timeout", ion.Int("timeout", 4))
			continue
		}

		if node.SelectionScore >= config.MaxSelectionScore {
			logger().Warn(context.Background(), "Skipping node due to timeout", ion.Int("timeout", 3))
			continue
		}

		// Check reputation score (optional filter)
		if config.MinReputationScore > 0 && node.ReputationScore < config.MinReputationScore {
			logger().Warn(context.Background(), "Skipping node due to timeout", ion.Int("timeout", 2))
			continue
		}

		// // Check if node is online (within timeout window)
		// if now.Sub(node.LastSeen) > timeoutDuration {
		// 	logger().Warn(context.Background(), "Skipping node due to timeout", ion.Int("timeout", 1))
		// 	continue
		// }

		// Node passed all filters
		eligible = append(eligible, *node)
	}

	return eligible
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
