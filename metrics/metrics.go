package metrics

import (
    "fmt"
    "net/http"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

// DefaultRegistry is the default Prometheus registry used by the application
var DefaultRegistry = prometheus.NewRegistry()

// Create a factory that uses our DefaultRegistry
var factory = promauto.With(prometheus.WrapRegistererWithPrefix("p2p_", DefaultRegistry))

// GetLibp2pRegisterer returns a registerer suitable for libp2p metrics
func GetLibp2pRegisterer() prometheus.Registerer {
    // This creates a registerer that will add the "libp2p_" prefix to all metrics
    return prometheus.WrapRegistererWithPrefix("libp2p_", DefaultRegistry)
}

var (
    // Node connection metrics
    ConnectedPeersGauge = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "p2p_connected_peers_total",
        Help: "The total number of currently connected peers",
    })

    ManagedPeersGauge = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "p2p_managed_peers_total",
        Help: "The total number of managed peers",
    })

    ActivePeersGauge = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "p2p_active_peers_total",
        Help: "The number of active (responding) peers",
    })

    // Heartbeat metrics
    HeartbeatSentCounter = promauto.NewCounter(prometheus.CounterOpts{
        Name: "p2p_heartbeats_sent_total",
        Help: "The total number of heartbeats sent",
    })

    HeartbeatReceivedCounter = promauto.NewCounter(prometheus.CounterOpts{
        Name: "p2p_heartbeats_received_total",
        Help: "The total number of heartbeats received",
    })

    HeartbeatFailedCounter = promauto.NewCounter(prometheus.CounterOpts{
        Name: "p2p_heartbeats_failed_total",
        Help: "The total number of failed heartbeats",
    })

    HeartbeatLatency = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "p2p_heartbeat_latency_seconds",
            Help:    "Latency of heartbeat responses in seconds",
            Buckets: prometheus.DefBuckets,
        },
        []string{"peer_id"},
    )

    // Message metrics
    MessagesSentCounter = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "p2p_messages_sent_total",
            Help: "The total number of messages sent",
        },
        []string{"protocol", "peer_id"},
    )

    MessagesReceivedCounter = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "p2p_messages_received_total",
            Help: "The total number of messages received",
        },
        []string{"protocol", "peer_id"},
    )

    MessageSizeHistogram = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "p2p_message_size_bytes",
            Help:    "Size of messages in bytes",
            Buckets: []float64{64, 256, 1024, 4096, 16384, 65536, 262144, 1048576},
        },
        []string{"protocol", "direction"},
    )

    // File transfer metrics
    FileTransferBytesCounter = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "p2p_file_transfer_bytes_total",
            Help: "The total number of bytes transferred for files",
        },
        []string{"direction", "peer_id"},
    )

    FileTransferDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "p2p_file_transfer_duration_seconds",
            Help:    "Duration of file transfers in seconds",
            Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
        },
        []string{"direction", "peer_id"},
    )

	FileTransferSpeedMBPS = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "file_transfer_speed_mbps",
            Help:    "File transfer speed in MB/s",
            Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
        },
        []string{"direction", "peer_id"},
    )

    // Database metrics
    DatabaseOperations = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "p2p_database_operations_total",
            Help: "The total number of database operations",
        },
        []string{"operation", "result"},
    )

    DatabaseLatency = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "p2p_database_operation_latency_seconds",
            Help:    "Latency of database operations in seconds",
            Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
        },
        []string{"operation"},
    )

	LogEntries = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "p2p_log_entries_total",
            Help: "Total number of log entries",
        },
        []string{"level", "component"},
    )
)

var PeerRemovedCounter = promauto.NewCounterVec(
    prometheus.CounterOpts{
        Name: "p2p_peers_removed_total",
        Help: "Total number of peers removed by reason",
    },
    []string{"reason"},
)

// StartMetricsServer starts the HTTP server for Prometheus metrics
func StartMetricsServer(addr string) {
    http.Handle("/metrics", promhttp.Handler())
    go func() {
        if err := http.ListenAndServe(addr, nil); err != nil {
            fmt.Printf("Error starting metrics server: %v\n", err)
        }
    }()
}