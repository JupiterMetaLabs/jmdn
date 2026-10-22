package metrics

import (
	"context"
	"log"
	"net/http"
	"time"

	"gossipnode/config/GRO"
	"gossipnode/metrics/common"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	LocalGRO interfaces.LocalGoroutineManagerInterface
)

// DefaultRegistry is the default Prometheus registry used by the application
var DefaultRegistry = prometheus.NewRegistry()

// Create a factory that uses our DefaultRegistry
var factory = promauto.With(DefaultRegistry)

// Verification sites for ConsensusLegacyVotesTotal's "site" label. Fixed
// strings chosen here, never derived from a message, so the label is bounded.
const (
	LegacyVoteSiteSequencer   = "sequencer"    // Sequencer per-vote alerting loop
	LegacyVoteSiteCommitteeV2 = "committee_v2" // seated-committee certificate tally
	LegacyVoteSitePoolTally   = "pool_tally"   // countEligibleYes: legacy and pool-wide tallies
)

// GetLibp2pRegisterer returns a registerer suitable for libp2p metrics
func GetLibp2pRegisterer() prometheus.Registerer {
	// This creates a registerer that will add the "libp2p_" prefix to all metrics
	return prometheus.WrapRegistererWithPrefix("libp2p_", DefaultRegistry)
}

var (
	// HandlerPanicsCounter counts panics recovered at a network-ingress boundary
	// (libp2p stream handler, pubsub message handler, HTTP/RPC worker). Label
	// "where" is a fixed handler name / protocol ID — bounded, never taken from
	// a message.
	HandlerPanicsCounter = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "jmdn_handler_panics_recovered_total",
			Help: "Panics recovered at a network-ingress boundary, by handler",
		},
		[]string{"where"},
	)

	// Node connection metrics
	ConnectedPeersGauge = factory.NewGauge(prometheus.GaugeOpts{
		Name: "p2p_connected_peers_total",
		Help: "The total number of currently connected peers",
	})

	ManagedPeersGauge = factory.NewGauge(prometheus.GaugeOpts{
		Name: "p2p_managed_peers_total",
		Help: "The total number of managed peers",
	})

	ActivePeersGauge = factory.NewGauge(prometheus.GaugeOpts{
		Name: "p2p_active_peers_total",
		Help: "The number of active (responding) peers",
	})

	// ConsensusLegacyVotesTotal counts BLS votes that verified only as a legacy
	// unbound "vote:<v>" signature instead of the block-bound v3 message.
	//
	// It counts DETECTION, not acceptance. With reject_legacy_votes on (the
	// default) such a vote is counted here and then refused — that is the
	// point: the metric is how an operator finds validators still signing the
	// legacy domain, and a metric that only fired once the flag was already
	// off would arrive too late to be used for anything.
	//
	// The "site" label names the verification layer that saw the vote, because
	// the three layers do not see the same traffic: only a sequencer runs
	// "sequencer", and only a node checking a certificate runs the other two.
	// A vote seen by two layers is counted once under each, so read the
	// per-site series rather than summing them.
	ConsensusLegacyVotesTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "consensus_legacy_votes_total",
		Help: "BLS votes that verified only as a legacy unbound signature, by verification site",
	}, []string{"site"})

	// Heartbeat metrics
	HeartbeatSentCounter = factory.NewCounter(prometheus.CounterOpts{
		Name: "p2p_heartbeats_sent_total",
		Help: "The total number of heartbeats sent",
	})

	HeartbeatReceivedCounter = factory.NewCounter(prometheus.CounterOpts{
		Name: "p2p_heartbeats_received_total",
		Help: "The total number of heartbeats received",
	})

	HeartbeatFailedCounter = factory.NewCounter(prometheus.CounterOpts{
		Name: "p2p_heartbeats_failed_total",
		Help: "The total number of failed heartbeats",
	})

	HeartbeatLatency = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "p2p_heartbeat_latency_seconds",
			Help:    "Latency of heartbeat responses in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"peer_id"},
	)

	// Message metrics
	MessagesSentCounter = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "p2p_messages_sent_total",
			Help: "The total number of messages sent",
		},
		[]string{"protocol", "peer_id"},
	)

	MessagesReceivedCounter = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "p2p_messages_received_total",
			Help: "The total number of messages received",
		},
		[]string{"protocol", "peer_id"},
	)

	MessageSizeHistogram = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "p2p_message_size_bytes",
			Help:    "Size of messages in bytes",
			Buckets: []float64{64, 256, 1024, 4096, 16384, 65536, 262144, 1048576},
		},
		[]string{"protocol", "direction"},
	)

	// BlocksRejectedCounter counts remotely-received blocks dropped by the
	// fail-closed validation gate before any forwarding, mutation, or
	// persistence. The "reason" label identifies which check failed
	// (e.g. no_certificate, quorum_not_met, bad_signature, bad_nonce) and is a
	// key signal for monitoring rejected blocks.
	BlocksRejectedCounter = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "p2p_blocks_rejected_total",
			Help: "The total number of remote blocks rejected by validation before processing",
		},
		[]string{"reason", "peer_id"},
	)

	// (File transfer metrics are not registered.)

	// Database metrics
	DatabaseOperations = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "p2p_database_operations_total",
			Help: "The total number of database operations",
		},
		[]string{"operation", "result"},
	)

	DatabaseLatency = factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "p2p_database_operation_latency_seconds",
			Help:    "Latency of database operations in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		},
		[]string{"operation"},
	)

	LogEntries = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "p2p_log_entries_total",
			Help: "Total number of log entries",
		},
		[]string{"level", "component"},
	)
)

var PeerRemovedCounter = factory.NewCounterVec(
	prometheus.CounterOpts{
		Name: "p2p_peers_removed_total",
		Help: "Total number of peers removed by reason",
	},
	[]string{"reason"},
)

// THIS NEED TO BE REVIEWED ONCE - REVIEW
// StartMetricsServer starts the HTTP server for Prometheus metrics
func StartMetricsServer(addr string) {
	if LocalGRO == nil {
		var err error
		LocalGRO, err = common.InitializeGRO(GRO.MetricsLocal)
		if err != nil {
			log.Printf("Error initializing LocalGRO: %v\n", err)
			return
		}
	}
	// Use our custom registry instead of the default one.
	//
	// SECURITY: serve a PRIVATE mux, never http.DefaultServeMux. The profiler's
	// `net/http/pprof` import registers its handlers on DefaultServeMux as an
	// import side effect, so a metrics server that inherited it would expose
	// /debug/pprof/* (heap contents, goroutine stacks, process cmdline) on the
	// metrics port — unauthenticated, and regardless of ports.profiler.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(DefaultRegistry, promhttp.HandlerOpts{}))

	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	serverErr := make(chan error, 1)

	// Start server in a separate goroutine managed by orchestrator
	_ = GoTracked(LocalGRO, GRO.MetricsApp, GRO.MetricsLocal, GRO.MetricsServerThread, func(ctx context.Context) error {
		err := server.ListenAndServe()
		select {
		case serverErr <- err:
		case <-ctx.Done():
			// Context cancelled, channel receiver may be gone
		}
		return nil
	})

	// Monitor context cancellation and server errors
	_ = GoTracked(LocalGRO, GRO.MetricsApp, GRO.MetricsLocal, GRO.RecordMetricsThread, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			// Context cancelled - shutdown gracefully with timeout
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(shutdownCtx)
			// Drain the error channel if server sent one
			select {
			case <-serverErr:
			default:
			}
			return ctx.Err()
		case err := <-serverErr:
			// Server stopped (error or normal shutdown)
			if err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		}
	})
}
