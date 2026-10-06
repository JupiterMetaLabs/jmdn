// MODULE: messaging/authorized_voters_tally
// PURPOSE: The VOTER-set source for a buddy's tally — who it COUNTS a vote
// from — as distinct from authorized_committee_tally.go's committee-seat
// source — who may SIGN a buddy result. "Count every validator's vote in the
// buddy tally again" LLD, change C6.
//
// THE DEFECT THIS FIXES: AuthorizedCommitteeForTallyAtHeight (the previous,
// sole source wired into Structs.ProcessVotesFromCRDT) resolves to
// eligibleMembers()/eligibleMembersUncapped() — the COMMITTEE pool, i.e. the
// at-most-7 seats VerifyCertificateForRound certifies against. A validator
// that is eligible but not seated was never counted even when its vote
// reached VoteCRDTLayer, because the tally's own authorized set excluded it.
// This file gives the tally a wider set: every snapshot-registered,
// BLS-keyed validator — not just the seated seven.
//
// ROLLBACK CONTRACT:
//
//	JMDN_VALIDATOR_VOTER_SET off -> Structs.authorizedVotersFor falls back to
//	                                 authorizedCommitteeFor: byte-identical
//	                                 to the pre-change wiring
//	JMDN_VALIDATOR_VOTER_SET on  -> AuthorizedVotersForTallyAtHeight below:
//	                                 the uncapped eligible pool, or the
//	                                 chain-anchored pool for that height
//
// This does not touch AuthorizedCommitteeForTally/AuthorizedCommitteeForTallyAtHeight,
// VerifyCertificateForRound, or any sequencer-side quorum/seat logic — those
// still resolve the committee exactly as before. Only the buddy's OWN tally
// decision (what IT concludes about a block) widens; quorum over seated
// buddies' signed results is unchanged.
package messaging

// AuthorizedVotersForTallyAtHeight is the set whose votes a buddy COUNTS at
// height: the full eligible validator pool (uncapped), or the chain-anchored
// pool for that height's selection period when anchoring (W1) is live for
// it. NOT the committee seats — those sign buddy RESULTS; validators vote.
// Wired via Structs.SetAuthorizedVotersForHeightFn. Fail-closed: an
// underlying error is returned unchanged, same contract as
// AuthorizedCommitteeForTallyAtHeight.
func AuthorizedVotersForTallyAtHeight(height uint64) (map[string]string, error) {
	if pool, handled, err := AnchoredPoolForHeight(height, true); handled {
		return pool, err
	}
	return eligibleMembersUncapped()
}
