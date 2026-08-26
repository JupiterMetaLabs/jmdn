package metrics

import "github.com/prometheus/client_golang/prometheus"

// ReputationEquivocationsReportedCounter is Design A of the A4-COMPLETION-LLD.md
// §3.3 cross-node visibility net: it does not close the "sequencer's own
// local CRDT copy might not have converged yet" gap, it makes it observable.
// Every node that reports the same real equivocation increments its own
// counter independently -- comparing this value across nodes' /metrics
// endpoints for the same peer_id is how an operator would notice the
// sequencer's count lagging its buddies', which is the only symptom that
// gap would ever produce.
var ReputationEquivocationsReportedCounter = factory.NewCounterVec(
	prometheus.CounterOpts{
		Name: "reputation_equivocations_reported_total",
		Help: "The total number of vote-CRDT equivocations reported to the local reputation store, by peer",
	},
	[]string{"peer_id"},
)

// ReputationNodeIsSequencerGauge (A4-COMPLETION-LLD.md §3.4's ordering
// mechanism) labels this node's own metrics 0/1 for "am I the sequencer" --
// set on every reputation-push tick from seednode.IsSequencer(). Exists so
// ops/prometheus/reputation-divergence-alert.rules.yml can select the
// sequencer's own reputation_equivocations_reported_total series apart from
// every buddy node's, without needing a separate operator-maintained
// relabel_configs rule to identify which scrape target is the sequencer.
var ReputationNodeIsSequencerGauge = factory.NewGauge(
	prometheus.GaugeOpts{
		Name: "reputation_node_is_sequencer",
		Help: "1 if this node currently holds a registered sequencer sign key, 0 otherwise",
	},
)
