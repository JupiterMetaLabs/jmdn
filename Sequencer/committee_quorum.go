// MODULE: Sequencer/committee_quorum
// PURPOSE: Size committee FORMATION by the BFT quorum, not by the full
// committee size.
//
// THE DEFECT THIS FIXES: block production required EVERY eligible committee
// member to be connected before a round could start (`len(MainCandidates) <
// config.MaxMainPeers` and friends), while VOTING only ever needed
// ByzantineQuorum(n) = ceil(2n/3). So a system engineered to tolerate f
// Byzantine voters could not tolerate ONE absent one at formation: a single
// node restart halted the chain fleet-wide. Observed 2026-08-04 — 24 eligible
// peers, 4 of 5 committee members connected, every round refused.
//
// WHY THIS IS SAFE (the property that must not break): the committee itself is
// UNCHANGED — still the fixed, deterministic eligible set (seed-signed snapshot,
// hard-capped by consensus.max_validators and trimmed by sorted peer_id). The
// certificate threshold is UNCHANGED — VerifyCertificate computes the quorum
// denominator n from the FLEET-AGREED committee (authenticatedCommittee: snapshot
// + fleet-uniform cap, NOT reduced by the local block_buddy blocklist — CON-12)
// and requires ByzantineQuorum(n). Nothing about verification moves. We only stop
// REFUSING TO START a round that could have reached quorum.
//
// Quorum intersection therefore still holds: two quorums of q = ceil(2n/3)
// drawn from the SAME fixed n intersect in >= 2q-n >= f+1 members, so at least
// one honest node is in both and two conflicting blocks cannot both certify.
// (n=7: q=5, intersection 3 >= f+1=3.) This is precisely why the committee must
// stay fixed — selecting a DIFFERENT committee per block from a larger pool
// would allow disjoint quorums and fork the chain. Do not "improve" this by
// widening max_validators beyond the voting set: the threshold is sized over
// the eligible set, so n=10 eligible with 7 voters needs 7 votes from 7 nodes
// while requiring ceil(20/3)=7 — see the cap comment in consensus_hardening.go.
//
// DEPLOYMENT: sequencer-only. Every hard gate this relaxes lives on the
// sequencer's proposal path; validators only warn on a short committee
// (ListenerHandler, CRDTSyncHandler). Certificates produced by a patched
// sequencer verify byte-identically on unpatched nodes, so this is NOT a flag
// day and rolls back by reverting one binary.
package Sequencer

import (
	"strings"

	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/messaging"

	"github.com/rs/zerolog/log"
)

// requiredMainPeers returns the minimum number of connected committee members
// needed to FORM a round: the BFT quorum over the authenticated eligible set,
// clamped to [1, config.MaxMainPeers].
//
// FALLBACK IS THE OLD BEHAVIOUR, DELIBERATELY: when the committee source is not
// pinned (legacy/unpinned deployments) or is unavailable, this returns
// config.MaxMainPeers — byte-identical to the pre-change gate. A node that
// cannot authenticate its committee does not get a laxer formation rule.
func requiredMainPeers() int {
	if !settings.IsLoaded() {
		return config.MaxMainPeers
	}
	if strings.TrimSpace(settings.Get().Consensus.SeedAuthorityBLSPub) == "" {
		// Unpinned (legacy) selection — unchanged.
		return config.MaxMainPeers
	}

	eligible, err := messaging.EligibleCommitteePeerIDs()
	if err != nil || len(eligible) == 0 {
		// Fail to the STRICTER rule, not the laxer one.
		log.Warn().Err(err).
			Msg("committee quorum: eligible set unavailable, requiring the full committee (unchanged behaviour)")
		return config.MaxMainPeers
	}

	q := messaging.ByzantineQuorum(len(eligible))

	// Never require more than the sequencer would actually select: MainCandidates
	// is capped at MaxMainPeers, so demanding more than that is unsatisfiable.
	if q > config.MaxMainPeers {
		q = config.MaxMainPeers
	}
	if q < 1 {
		q = 1
	}
	return q
}
