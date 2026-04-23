package Maps

import (
	"log"
	"sync"
)

// Global map to store vote results from buddy nodes: map[peerID]voteResult
var voteResultsMap = make(map[string]int8)

// Mutex to protect voteResultsMap
var voteResultsMutex sync.Mutex

// StoreVoteResult stores a vote result from a buddy node
func StoreVoteResult(peerID string, result int8) {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	voteResultsMap[peerID] = result
	log.Printf("Stored vote result for peer %s: %d", peerID, result)
}

// GetVoteResult retrieves a vote result for a peer
func GetVoteResult(peerID string) (int8, bool) {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	result, exists := voteResultsMap[peerID]
	return result, exists
}

// GetAllVoteResults retrieves all vote results
func GetAllVoteResults() map[string]int8 {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	result := make(map[string]int8)
	for k, v := range voteResultsMap {
		result[k] = v
	}
	return result
}

// ClearVoteResults clears all vote results
func ClearVoteResults() {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	voteResultsMap = make(map[string]int8)
	log.Printf("Cleared all vote results")
}

// GetVoteResultsCount returns the number of stored vote results
func GetVoteResultsCount() int {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	return len(voteResultsMap)
}
