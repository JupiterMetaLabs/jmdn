package DB_OPs

import (
	"context"
	"fmt"
	"sync"
	"time"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// ----------------------------------------------------------------------------
// LogWriter
// ----------------------------------------------------------------------------

// LogWriter stores EVM-emitted logs in ImmuDB and fans them out to live
// WebSocket subscribers.  The zero value is NOT usable; use GlobalLogWriter.
type LogWriter struct {
	mu   sync.RWMutex
	subs map[chan *ethtypes.Log]struct{}
}

// GlobalLogWriter is the package-level singleton.  It is ready to use
// immediately — no Init() call is required.
var GlobalLogWriter = &LogWriter{
	subs: make(map[chan *ethtypes.Log]struct{}),
}

// Write persists each log in ImmuDB under three compound key schemes and then
// fans the log out to all active subscribers (non-blocking; drops if channel full).
//
// Key schema
//   Primary:   log:{blockNumber}:{txIndex}:{logIndex}
//   By addr:   logaddr:{addrHex}:{blockNumber}:{logIndex}
//   By topic:  logtopic:{topicHex}:{blockNumber}:{logIndex}   (one per topic)
func (lw *LogWriter) Write(logs []*ethtypes.Log) error {
	if len(logs) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("LogWriter.Write: failed to get DB handle: %w", err)
	}

	// Store all logs via ThebeDB in a single call.
	if storeErr := h.StoreLogs(ctx, logs); storeErr != nil {
		return fmt.Errorf("LogWriter.Write: StoreLogs failed: %w", storeErr)
	}

	for _, l := range logs {
		if l == nil {
			continue
		}

		// Fan-out to live subscribers (non-blocking)
		lw.fanOut(l)
	}

	return nil
}

// fanOut sends log l to every active subscriber channel.
// Subscribers whose buffer is full are silently skipped (they are too slow).
func (lw *LogWriter) fanOut(l *ethtypes.Log) {
	lw.mu.RLock()
	defer lw.mu.RUnlock()
	for ch := range lw.subs {
		select {
		case ch <- l:
		default:
			// Channel full — drop rather than block EVM execution
		}
	}
}

// Subscribe returns a buffered, read-only channel that receives every log
// written via Write().  The caller MUST call Unsubscribe when done to avoid
// a goroutine/memory leak.
func (lw *LogWriter) Subscribe() <-chan *ethtypes.Log {
	ch := make(chan *ethtypes.Log, 256)
	lw.mu.Lock()
	lw.subs[ch] = struct{}{}
	lw.mu.Unlock()
	return ch
}

// Unsubscribe removes and closes the channel returned by Subscribe.
// It is safe to call Unsubscribe more than once for the same channel.
func (lw *LogWriter) Unsubscribe(ch <-chan *ethtypes.Log) {
	// We need the bidirectional handle to close and delete.
	// The internal map stores chan *ethtypes.Log (bidirectional).
	lw.mu.Lock()
	defer lw.mu.Unlock()
	for stored := range lw.subs {
		// Compare channel identity via interface equality
		if fmt.Sprintf("%p", stored) == fmt.Sprintf("%p", ch) {
			close(stored)
			delete(lw.subs, stored)
			return
		}
	}
}
