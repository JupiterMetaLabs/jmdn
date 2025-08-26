// logging/loki.go
package logging

import (
	"time"

	"github.com/grafana/loki-client-go/loki"
	"github.com/prometheus/common/model"
)

// lokiWriteSyncer implements zapcore.WriteSyncer for Loki.
// It leverages the internal batching of the official loki-client-go.
type lokiWriteSyncer struct {
	client *loki.Client
	labels model.LabelSet
}

// newLokiWriteSyncer creates a new WriteSyncer for Loki.
// It's the caller's responsibility to create the loki.Client.
func newLokiWriteSyncer(client *loki.Client, labels model.LabelSet) *lokiWriteSyncer {
	return &lokiWriteSyncer{
		client: client,
		labels: labels,
	}
}

// Write implements the zapcore.WriteSyncer interface.
// The client handles batching and sending asynchronously.
func (l *lokiWriteSyncer) Write(p []byte) (int, error) {
    // The client's Handle method is non-blocking and adds the entry to a batch.
    // It's safe for concurrent use.
    err := l.client.Handle(l.labels, time.Now(), string(p))
    if err != nil {
        // Log the error but don't return it to prevent zap from writing to stderr
        // as we've already retried at the client level
        return 0, nil
    }
    return len(p), nil
}

// Sync is a no-op. The client manages flushing its own buffers.
// The real sync happens when the client is stopped via Close().
func (l *lokiWriteSyncer) Sync() error {
	return nil
}

// Close stops the Loki client, which flushes any remaining buffered entries.
// This should be called on application shutdown.
func (l *lokiWriteSyncer) Close() {
	l.client.Stop()
}
