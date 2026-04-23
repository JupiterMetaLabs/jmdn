package helper

import (
	"fmt"

	PubSubMessages "gossipnode/config/PubSubMessages"
)

// @static function
/*
This function checks the reachability of the candidates and returns the main and backup candidates.
What it does:
- Pings the candidates and adds them to the cache
- Returns the main and backup candidates
*/
func CheckReachability(candidates []PubSubMessages.Buddy_PeerMultiaddr, mainLen int) ([]PubSubMessages.Buddy_PeerMultiaddr, []PubSubMessages.Buddy_PeerMultiaddr, error) {

	if candidates == nil {
		return nil, nil, fmt.Errorf("REACHABILITY: candidates are nil")
	}

	main, backup := InitCandidateLists(len(candidates))
	for _, candidate := range candidates {
		if err := PingAndAddToCache(candidate); err != nil {
			return nil, nil, fmt.Errorf("REACHABILITY: failed to ping and add to cache: %v", err)
		}
		if len(main) >= mainLen {
			backup = append(backup, candidate)
		} else {
			main = append(main, candidate)
		}
	}

	return main, backup, nil
}
