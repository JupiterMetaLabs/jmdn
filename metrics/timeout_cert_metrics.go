package metrics

import "github.com/prometheus/client_golang/prometheus"

// TimeoutCertificatesFormedCounter counts every TimeoutCertificate this node
// itself locally builds and accepts (messaging/timeout_gossip.go's
// tryCertify, on the AcceptTimeoutCertificate success path) once quorum is
// reached over collected TimeoutVotes. Only active while
// JMDN_TIMEOUT_CERT_WIRING is on -- with the flag off tryCertify is
// unreachable, so this stays at 0. An operator watching this stuck at 0
// during a live incident (with the flag on) knows quorum is never being
// reached locally, not that the flow is silently working.
var TimeoutCertificatesFormedCounter = factory.NewCounter(
	prometheus.CounterOpts{
		Name: "timeout_certificates_formed_total",
		Help: "The total number of TimeoutCertificates this node has locally built and accepted after reaching quorum",
	},
)

// TimeoutPeriodsAdvancedCounter counts every successful PeriodStore period
// advance driven by a TimeoutCertificate, split by which of the two paths
// produced it: "local" is this node's own locally-built certificate
// (tryCertify, after it reaches quorum itself); "gossip_or_rejoin" is a
// certificate accepted directly from elsewhere via
// AcceptIncomingTimeoutCertificate -- gossiped in today, or (once a rejoin
// RPC exists) fetched on catch-up. The split lets an operator tell whether a
// stalled height is recovering through local quorum or only through
// certificates borrowed from peers who are already ahead.
var TimeoutPeriodsAdvancedCounter = factory.NewCounterVec(
	prometheus.CounterOpts{
		Name: "timeout_periods_advanced_total",
		Help: "The total number of PeriodStore period advances driven by an accepted TimeoutCertificate, by source",
	},
	[]string{"source"},
)

// TimeoutVotesReceivedCounter counts every TimeoutVote recorded into the
// local per-round collector (messaging/timeout_gossip.go's
// recordAndMaybeCertify), which is the single choke point for all three
// production call sites: this node's own self-vote from
// MaybeStartTimeoutFlow, a peer's vote relayed by
// handleTimeoutVoteBroadcast, and this node's own request-triggered vote
// signed from timeout_request.go. Not source-labelled because labelling
// would require threading a new parameter through recordAndMaybeCertify and
// all of its callers; a flat total is enough to tell "votes are arriving at
// all" from "nothing is arriving."
var TimeoutVotesReceivedCounter = factory.NewCounter(
	prometheus.CounterOpts{
		Name: "timeout_votes_received_total",
		Help: "The total number of TimeoutVotes recorded into the local per-round collector",
	},
)

// TimeoutRequestsReceivedCounter counts every TimeoutRequest that passed
// VerifyTimeoutRequest in handleTimeoutRequestBroadcast (messaging/
// timeout_request.go) -- i.e. every request this node accepted as genuinely
// signed by the pinned sequencer for this node's chain, before the separate
// decideTimeoutRequest policy check (round-lock, staleness, etc.) decides
// whether to actually sign. A verified-request count that never turns into
// signed votes points at decideTimeoutRequest declining them, not at
// requests failing signature verification.
var TimeoutRequestsReceivedCounter = factory.NewCounter(
	prometheus.CounterOpts{
		Name: "timeout_requests_received_total",
		Help: "The total number of TimeoutRequests that passed signature verification",
	},
)
