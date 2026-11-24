package metrics

import "github.com/prometheus/client_golang/prometheus"

// ValidatorVoteIngestCounter ("Count every validator's vote in the buddy
// tally again" LLD, §4c) tracks what IngestValidatorVote does with every
// received wire vote — the v2, block-keyed CRDT write that is the only
// keyspace a buddy's tally reads. Labels:
//   - result: "stored" (the write to VoteCRDTLayer succeeded — NOT the same
//     as counted: a vote only becomes counted at tally time, via
//     avcvotes.TallyBlock, and only if the sender is in that tally's
//     authorized voter set; see Structs.processVotesFromCRDT_v2's own
//     per-block "Tallied block vote CRDT" log line for the actual count),
//     "unsigned" (ErrUnsignedVote), "invalid" (malformed/missing fields),
//     "compacted" (ErrHeightCompacted — expected/harmless, a late vote),
//     "disabled" (ValidatorVoteIngestEnabled off), "write_error" (AddVote
//     failed for any other reason, e.g. the per-peer ingest cap)
//   - path: "direct" (ListenerHandler.go's direct-stream receive) or
//     "pubsub" (subscriptionService.go's consensus-channel receive)
//
// "stored" rising while the tally's own "counted_peers" log line stays flat
// is how a gap between "votes arriving" and "votes actually weighed by a
// buddy" (e.g. the sender isn't in V) becomes visible instead of silent —
// exactly the kind of gap this LLD exists to close.
var ValidatorVoteIngestCounter = factory.NewCounterVec(
	prometheus.CounterOpts{
		Name: "validator_vote_ingest_total",
		Help: "Outcomes of ingesting a received validator vote into the v2 block-keyed vote CRDT, by result and receive path",
	},
	[]string{"result", "path"},
)
