// logging/loki.go
package logging

import (
	"context"
	"fmt"
	"gossipnode/AVC/BFT/common"
	GRO "gossipnode/config/GRO"
	"sync"
	"time"

	"github.com/grafana/loki-client-go/loki"
	"github.com/prometheus/common/model"
)

// lokiWriteSyncer implements zapcore.WriteSyncer for Loki.
// It leverages the internal batching of the official loki-client-go.
type lokiWriteSyncer struct {
	client    *loki.Client
	labels    model.LabelSet
	ch        chan []byte
	wg        sync.WaitGroup
	closed    chan struct{}
	closeOnce sync.Once
	stopOnce  sync.Once
}

// newLokiWriteSyncer creates a new WriteSyncer for Loki.
// It's the caller's responsibility to create the loki.Client.
func newLokiWriteSyncer(client *loki.Client, labels model.LabelSet) *lokiWriteSyncer {
	lws := &lokiWriteSyncer{
		client: client,
		labels: labels,
		ch:     make(chan []byte, 1000), // Buffered channel to prevent blocking
		closed: make(chan struct{}),
	}
	lws.wg.Add(1)

	// Prefer orchestrator-managed goroutine, but fall back to a plain goroutine if unavailable.
	if LoggingLocalGRO == nil {
		var err error
		LoggingLocalGRO, err = common.InitializeGRO(GRO.LoggingLocal)
		if err != nil {
			// Logging should degrade gracefully.
			fmt.Printf("❌ Failed to initialize local gro: %v", err)
		}
	}

	if LoggingLocalGRO != nil {
		LoggingLocalGRO.Go(GRO.LoggingLokiThread, func(ctx context.Context) error {
			lws.processLogs(ctx)
			return nil
		})
	} else {
		go lws.processLogs(context.Background())
	}

	return lws
}

// Write implements the zapcore.WriteSyncer interface.
// This method is completely non-blocking and thread-safe.
func (l *lokiWriteSyncer) Write(p []byte) (int, error) {
	// Copy the data to avoid issues with zap reusing buffers
	cp := make([]byte, len(p))
	copy(cp, p)

	// Try to send to channel, but don't block if it's full
	select {
	case l.ch <- cp:
		// Successfully queued
	default:
		// Channel is full, drop the log entry to prevent blocking
		// This ensures we never block the calling thread
	}
	return len(p), nil
}

// processLogs processes log entries from the channel in a separate goroutine.
// This prevents any blocking in the Write method.
func (l *lokiWriteSyncer) processLogs(ctx context.Context) {
	defer l.wg.Done()

	for {
		select {
		case <-l.closed:
			// Drain any remaining logs before closing
			for {
				select {
				case p := <-l.ch:
					_ = l.client.Handle(l.labels, time.Now().UTC(), string(p))
				default:
					return
				}
			}
		case <-ctx.Done():
			// Best-effort drain and stop to avoid leaking background work on shutdown.
			for {
				select {
				case p := <-l.ch:
					_ = l.client.Handle(l.labels, time.Now().UTC(), string(p))
				default:
					l.stopOnce.Do(func() { l.client.Stop() })
					return
				}
			}
		case p := <-l.ch:
			// Process the log entry
			err := l.client.Handle(l.labels, time.Now().UTC(), string(p))
			if err != nil {
				// Silently drop errors to prevent blocking
				// The client has its own retry logic
			}
		}
	}
}

// Sync is a no-op. The client manages flushing its own buffers.
// The real sync happens when the client is stopped via Close().
func (l *lokiWriteSyncer) Sync() error {
	return nil
}

// Close stops the Loki client, which flushes any remaining buffered entries.
// This should be called on application shutdown.
func (l *lokiWriteSyncer) Close() {
	l.closeOnce.Do(func() {
		close(l.closed)
	})
	l.wg.Wait()
	l.stopOnce.Do(func() { l.client.Stop() })
}
