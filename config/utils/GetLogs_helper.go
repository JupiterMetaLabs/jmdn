package utils

import (
	"github.com/ethereum/go-ethereum/common"
)

// convertHashesToStrings converts []common.Hash to []string
func ConvertHashesToStrings(hashes []common.Hash) []string {
	strings := make([]string, len(hashes))
	for i, hash := range hashes {
		strings[i] = hash.Hex()
	}
	return strings
}

// containsAddress checks if the log address is in the filter addresses
func ContainsAddress(filterAddresses []string, logAddress string) bool {
	for _, addr := range filterAddresses {
		if addr == logAddress {
			return true
		}
	}
	return false
}

// matchesTopicFilter checks if log topics match the filter topics
func MatchesTopicFilter(filterTopics [][]string, logTopics []string) bool {
	// If no topics in filter, match all
	if len(filterTopics) == 0 {
		return true
	}

	// Check each topic position
	for i, filterTopicGroup := range filterTopics {
		// If we've exceeded the number of topics in the log, no match
		if i >= len(logTopics) {
			return false
		}

		// If filter topic group is empty, it matches any topic at this position
		if len(filterTopicGroup) == 0 {
			continue
		}

		// Check if log topic matches any topic in the filter group
		logTopic := logTopics[i]
		found := false
		for _, filterTopic := range filterTopicGroup {
			if filterTopic == logTopic {
				found = true
				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}
