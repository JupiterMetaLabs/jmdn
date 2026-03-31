package Maps

import (
	"log"
	"sync"
)

// voteResultsMap stores vote results scoped by block hash: map[blockHash]map[peerID]voteResult
var voteResultsMap = make(map[string]map[string]int8)

// Mutex to protect voteResultsMap
var voteResultsMutex sync.Mutex

// StoreVoteResult stores a vote result from a buddy node, scoped by block hash
func StoreVoteResult(blockHash, peerID string, result int8) {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	if voteResultsMap[blockHash] == nil {
		voteResultsMap[blockHash] = make(map[string]int8)
	}
	voteResultsMap[blockHash][peerID] = result
	log.Printf("Stored vote result for block %s, peer %s: %d", blockHash, peerID, result)
}

// GetVoteResult retrieves a vote result for a peer in a given round
func GetVoteResult(blockHash, peerID string) (int8, bool) {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	if round, exists := voteResultsMap[blockHash]; exists {
		result, ok := round[peerID]
		return result, ok
	}
	return 0, false
}

// GetAllVoteResults retrieves all vote results for a given block hash
func GetAllVoteResults(blockHash string) map[string]int8 {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	result := make(map[string]int8)
	if round, exists := voteResultsMap[blockHash]; exists {
		for k, v := range round {
			result[k] = v
		}
	}
	return result
}

// ClearVoteResults clears all vote results across all rounds
func ClearVoteResults() {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	voteResultsMap = make(map[string]map[string]int8)
	log.Printf("Cleared all vote results")
}

// ClearVoteResultsForBlock clears vote results for a specific block hash
func ClearVoteResultsForBlock(blockHash string) {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	delete(voteResultsMap, blockHash)
	log.Printf("Cleared vote results for block %s", blockHash)
}

// GetVoteResultsCount returns the number of stored vote results for a given block hash
func GetVoteResultsCount(blockHash string) int {
	voteResultsMutex.Lock()
	defer voteResultsMutex.Unlock()
	if round, exists := voteResultsMap[blockHash]; exists {
		return len(round)
	}
	return 0
}
