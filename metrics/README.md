# Metrics Module

## Overview

The Metrics module provides Prometheus-compatible metrics collection for the JMZK network. It tracks system performance, peer connections, message statistics, and database operations.

## Purpose

The Metrics module enables:
- Prometheus-compatible metrics collection
- System performance monitoring
- Peer connection tracking
- Message statistics
- Database operation metrics
- File transfer metrics

## Key Components

### 1. Metrics Collection
**File:** `metrics.go`

Main metrics collection:
- `StartMetricsServer`: Start Prometheus metrics server
- `UpdateMainDBConnectionPoolMetrics`: Update main DB pool metrics
- `UpdateAccountsDBConnectionPoolMetrics`: Update accounts DB pool metrics

### 2. Metrics Definitions

**Node Connection Metrics:**
- `ConnectedPeersGauge`: Number of connected peers
- `ManagedPeersGauge`: Number of managed peers
- `ActivePeersGauge`: Number of active peers

**Heartbeat Metrics:**
- `HeartbeatSentCounter`: Number of heartbeats sent
- `HeartbeatReceivedCounter`: Number of heartbeats received
- `HeartbeatFailedCounter`: Number of failed heartbeats
- `HeartbeatLatency`: Heartbeat latency histogram

**Message Metrics:**
- `MessagesSentCounter`: Number of messages sent
- `MessagesReceivedCounter`: Number of messages received
- `MessageSizeHistogram`: Message size histogram

**File Transfer Metrics:**
- `FileTransferBytesCounter`: Bytes transferred
- `FileTransferDuration`: Transfer duration histogram
- `FileTransferSpeedMBPS`: Transfer speed histogram

**Database Metrics:**
- `DatabaseOperations`: Database operation counter
- `DatabaseLatency`: Database operation latency histogram
- `MainDBConnectionPoolCount`: Main DB pool count
- `MainDBConnectionPoolActive`: Main DB pool active connections
- `MainDBConnectionPoolIdle`: Main DB pool idle connections
- `AccountsDBConnectionPoolCount`: Accounts DB pool count
- `AccountsDBConnectionPoolActive`: Accounts DB pool active connections
- `AccountsDBConnectionPoolIdle`: Accounts DB pool idle connections

**Logging Metrics:**
- `LogEntries`: Log entry counter

**Peer Management Metrics:**
- `PeerRemovedCounter`: Peer removal counter

## Key Functions

### Start Metrics Server

```go
// Start Prometheus metrics server
func StartMetricsServer(addr string) {
    // Start HTTP server on /metrics endpoint
    // Serve Prometheus metrics
}
```

### Update Connection Pool Metrics

```go
// Update main DB connection pool metrics
func UpdateMainDBConnectionPoolMetrics(total, active, idle int) {
    MainDBConnectionPoolCount.Set(float64(total))
    MainDBConnectionPoolActive.Set(float64(active))
    MainDBConnectionPoolIdle.Set(float64(idle))
}
```

## Usage

### Start Metrics Server

```go
import "gossipnode/metrics"

// Start metrics server
metrics.StartMetricsServer(":8081")
```

### Access Metrics

```bash
# Access Prometheus metrics
curl http://localhost:8081/metrics
```

### Update Metrics

```go
// Update peer metrics
metrics.ConnectedPeersGauge.Set(float64(peerCount))

// Increment message counter
metrics.MessagesSentCounter.WithLabelValues("protocol", "peerID").Inc()

// Record latency
metrics.DatabaseLatency.WithLabelValues("operation").Observe(duration.Seconds())
```

## Integration Points

### All Modules
- All modules use metrics for monitoring
- Consistent metrics format across modules

### Node Module
- Tracks peer connections
- Monitors heartbeat performance

### Messaging Module
- Tracks message statistics
- Monitors message throughput

### Database (DB_OPs)
- Tracks database operations
- Monitors connection pool status

### Transfer Module
- Tracks file transfer performance
- Monitors transfer speeds

## Configuration

Metrics server port can be configured via command-line flag:

```bash
./jmdn -metrics 8081  # Default port
```

## Error Handling

The module includes comprehensive error handling:
- Metrics server startup errors
- Metric update errors
- Registry errors

## Logging

Metrics operations are logged to:
- Application logs
- Metrics server logs
- Error logs

## Security

- Metrics endpoint access control
- Metric data sanitization
- Secure metric collection

## Performance

- Efficient metric collection
- Low overhead metric updates
- Optimized metric storage
- Fast metric queries

## Testing

Test files:
- `metrics_test.go`: Metrics operation tests
- Integration tests
- Performance tests

## Future Enhancements

- Enhanced metric types
- Advanced metric aggregation
- Better error handling
- Performance optimizations
- Additional metric categories

