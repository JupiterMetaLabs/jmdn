package selection

import (
	"fmt"
	"time"
)

// FilterConfig defines filtering rules
type FilterConfig struct {
	MinReputationScore float64
	MinSelectionScore  float64 // Minimum selection score (0.5 <= score < 1.0)
	MaxSelectionScore  float64 // Maximum selection score (exclude score >= 1.0)
	NodeTimeoutMinutes int
	NodesPerASN        int // Target nodes per ASN for diversity
}

// DefaultFilterConfig returns sensible defaults
func DefaultFilterConfig() FilterConfig {
	return FilterConfig{
		MinReputationScore: 0.0, // Accept all reputation scores (or use 0.5 if you want)
		MinSelectionScore:  0.5, // Only nodes with score >= 0.5
		MaxSelectionScore:  1.0, // Exclude nodes with score >= 1.0 (perfect score = reserved/system nodes)
		NodeTimeoutMinutes: 10,
		NodesPerASN:        2, // Try to get 2 nodes per ASN
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
	// now := time.Now()
	// timeoutDuration := time.Duration(config.NodeTimeoutMinutes) * time.Minute

	for i := range nodes {
		node := &nodes[i]

		// Skip self
		if node.ID == myNodeID {
			fmt.Println("⚠️  Skipping node due to timeout:", -1)
			continue
		}

		// Check if node is active
		if !node.IsActive {
			fmt.Println("⚠️  Skipping node due to timeout:", 0)
			continue
		}

		// Check selection score (CRITICAL: 0.5 <= score < 1.0)
		if node.SelectionScore < config.MinSelectionScore {
			fmt.Println("⚠️  Skipping node due to timeout:", 4)
			continue
		}

		if node.SelectionScore >= config.MaxSelectionScore {
			fmt.Println("⚠️  Skipping node due to timeout:", 3)
			continue
		}

		// Check reputation score (optional filter)
		if config.MinReputationScore > 0 && node.ReputationScore < config.MinReputationScore {
			fmt.Println("⚠️  Skipping node due to timeout:", 2)
			continue
		}

		// // Check if node is online (within timeout window)
		// if now.Sub(node.LastSeen) > timeoutDuration {
		// 	fmt.Println("⚠️  Skipping node due to timeout:", 1)
		// 	continue
		// }

		// Check if ASN is valid (not unknown/empty)
		// Optionally skip nodes with unknown ASN for diversity
		// if node.ASN == "" || node.ASN == "UNKNOWN" || node.ASN == "AS-UNKNOWN" {
		// 	continue
		// }

		// Node passed all filters
		eligible = append(eligible, *node)
	}

	return eligible
}

// GroupNodesByASN groups nodes by their ASN
func GroupNodesByASN(nodes []Node) map[string][]Node {
	asnMap := make(map[string][]Node)

	for _, node := range nodes {
		asn := node.ASN
		if asn == "" || asn == "UNKNOWN" {
			asn = "AS-UNKNOWN"
		}
		asnMap[asn] = append(asnMap[asn], node)
	}

	return asnMap
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
}

// GetFilterStats returns detailed filtering statistics
func GetFilterStats(myNodeID string, nodes []Node, config FilterConfig) FilterStats {
	stats := FilterStats{
		TotalNodes: len(nodes),
	}

	now := time.Now()
	timeoutDuration := time.Duration(config.NodeTimeoutMinutes) * time.Minute

	for i := range nodes {
		node := &nodes[i]

		if node.ID == myNodeID {
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

	return stats
}
