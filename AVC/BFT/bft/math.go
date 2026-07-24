package bft

// HasQuorum checks if vote counts meet 2/3 quorum requirement using integer math.
// Returns true if yesVotes >= (2/3) * totalVotes
// Formula: yes*3 >= total*2  (avoids floating point arithmetic)
//
// Examples:
//   - HasQuorum(9, 13) = true  (9*3=27 >= 13*2=26)
//   - HasQuorum(8, 13) = false (8*3=24 < 13*2=26)
//   - HasQuorum(7, 10) = true  (7*3=21 >= 10*2=20)
func HasQuorum(yes, total int) bool {
	if total == 0 {
		return false
	}
	return yes*QuorumDenominator >= total*QuorumNumerator
}

// QuorumThreshold calculates the minimum number of votes needed for 2/3 quorum.
// Returns ceil((2/3) * total) using integer math.
//
// Formula: (total*2 + 2) / 3 (equivalent to ceil((2/3)*total))
//
// Examples:
//   - QuorumThreshold(13) = 9  ((13*2+2)/3 = 28/3 = 9)
//   - QuorumThreshold(10) = 7  ((10*2+2)/3 = 22/3 = 7)
//   - QuorumThreshold(12) = 8  ((12*2+2)/3 = 26/3 = 8)
func QuorumThreshold(total int) int {
	if total <= 0 {
		return 0
	}
	// Ceiling: (total*2 + 2) / 3
	return (total*QuorumNumerator + QuorumDenominator - 1) / QuorumDenominator
}

// BFTThreshold calculates the BFT consensus threshold using the 2f+1 rule,
// where f = (buddyCount - 1) / 3 (maximum Byzantine faults tolerated). 2f+1 is
// always <= buddyCount for buddyCount >= 1, so there is never a majority
// fallback (never a simple majority substituting for 2f+1).
//
// Examples:
//   - BFTThreshold(13) = 9  (f=4, 2*4+1=9)
//   - BFTThreshold(10) = 7  (f=3, 2*3+1=7)
//   - BFTThreshold(4)  = 3  (f=1, 2*1+1=3, 3 <= 4)
func BFTThreshold(buddyCount int) int {
	if buddyCount <= 0 {
		return 0
	}

	f := (buddyCount - 1) / ByzantineFactor
	// 2f+1 is always <= buddyCount for buddyCount >= 1 (2*floor((n-1)/3)+1 <=
	// (2n+1)/3 <= n), so no majority fallback is ever needed. The former
	// "if threshold > buddyCount => (n/2)+1" branch was unreachable and is
	// removed so a simple majority can never silently replace the 2f+1 threshold.
	return QuorumMultiplier*f + QuorumOffset
}

// ByzantineTolerance calculates the maximum number of Byzantine faults that can be tolerated.
// Returns (buddyCount - 1) / 3
//
// Examples:
//   - ByzantineTolerance(13) = 4  ((13-1)/3 = 4)
//   - ByzantineTolerance(10) = 3  ((10-1)/3 = 3)
//   - ByzantineTolerance(7)  = 2  ((7-1)/3 = 2)
func ByzantineTolerance(buddyCount int) int {
	if buddyCount <= 0 {
		return 0
	}
	return (buddyCount - 1) / ByzantineFactor
}
