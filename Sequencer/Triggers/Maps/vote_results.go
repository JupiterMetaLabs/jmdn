package Maps

import (
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

}

// ClearVoteResults clears all vote results
func ClearVoteResults() {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	voteResultsMap = make(map[string]int8)

}
