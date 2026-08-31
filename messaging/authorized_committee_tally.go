// MODULE: messaging/authorized_committee_tally
// PURPOSE: One flag-switched source for the BUDDY-SIDE vote tally's authorized
// set, so the buddy and the sequencer stop resolving committee membership from
// two independent places.
//
// THE DEFECT THIS FIXES: main.go wired Structs.SetAuthorizedCommitteeFn to
// messaging.AuthorizedCommittee, which is eligibleMembers() - the legacy,
// alphabetically-capped set - with no reference to CommitteeV2Enabled anywhere
// in that chain. Meanwhile VerifyCertificateForRound (committee_v2.go) DOES
// switch on the flag: with v2 on, the sequencer certifies against the seated
// committee SelectCommittee produced. So flipping JMDN_COMMITTEE_V2 on moved
// the sequencer's membership view and left the buddy's own tally on the old
// one - two sources of truth for one decision. Inert at today's shape
// (pool == seats == 7: both resolve to the same members), live the moment the
// pool exceeds the seat count.
//
// ROLLBACK CONTRACT - the whole point of this file:
//
//	JMDN_COMMITTEE_V2 off -> AuthorizedCommittee() -> eligibleMembers()
//	                         byte-identical to the pre-change wiring
//	JMDN_COMMITTEE_V2 on  -> eligibleMembersUncapped()
//	                         the pool SelectCommittee draws its seats from
//
// The legacy path is not modified, not wrapped, and not bypassed: with the flag
// off this function IS AuthorizedCommittee, reached through the same seam, with
// the same signature and the same error semantics. Turning the flag off is a
// complete rollback and requires no other action. Do not delete the legacy path
// until v2 is proven on testnet and a separate migration decision is made.
//
// WHY THE POOL AND NOT THE SEATED SUBSET: the seam Structs installs is
// func() (map[string]string, error) - it carries no round. Resolving the SEATED
// committee needs a RoundContext (PrevHash, Slot, Period), which needs the
// block, which the tally path (processVotesFromCRDT_v2: height + blockHash
// only) does not have. Widening that seam is a real change and is deliberately
// NOT done here. The pool is a SUPERSET of the seats, so this is permissive in
// exactly one direction: a buddy may weigh an eligible-but-unseated peer's vote
// when forming its OWN conclusion. It cannot inflate quorum - the sequencer
// counts seated buddies' signatures and nothing else (VerifyCertificateForRound
// -> TallyAgainst). At today's P == k the two sets are identical, so this is a
// no-op until the pool actually grows past the seat count.
package messaging

// AuthorizedCommitteeForTally is the committee source for the buddy-side vote
// tally (the `authorized` argument of avc/crdt/votes.TallyBlock), wired at
// startup via Structs.SetAuthorizedCommitteeFn.
//
// FAIL CLOSED on both paths: the underlying error is returned unchanged and
// callers MUST treat an error as "authorize nobody" - never as "try the other
// path". A feature flag does not get to widen the authorized set on failure.
func AuthorizedCommitteeForTally() (map[string]string, error) {
	if !CommitteeV2Enabled {
		// LEGACY PATH - unchanged, and the exact function main.go used before.
		return AuthorizedCommittee()
	}
	// V2 PATH - the pool SelectCommittee seats from.
	return eligibleMembersUncapped()
}
