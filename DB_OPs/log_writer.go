package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// logEntry is a JSON-serialisable mirror of types.Log.
// We serialise this to ImmuDB so that consumers outside this package
// (e.g. future explorers) can decode without importing go-ethereum.
type logEntry struct {
	Address     string   `json:"address"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data"`
	BlockNumber uint64   `json:"blockNumber"`
	TxHash      string   `json:"txHash"`
	TxIndex     uint     `json:"txIndex"`
	BlockHash   string   `json:"blockHash"`
	LogIndex    uint     `json:"logIndex"`
	Removed     bool     `json:"removed"`
}

func ethLogToEntry(l *ethtypes.Log) logEntry {
	topics := make([]string, len(l.Topics))
	for i, t := range l.Topics {
		topics[i] = t.Hex()
	}
	return logEntry{
		Address:     l.Address.Hex(),
		Topics:      topics,
		Data:        fmt.Sprintf("0x%x", l.Data),
		BlockNumber: l.BlockNumber,
		TxHash:      l.TxHash.Hex(),
		TxIndex:     l.TxIndex,
		BlockHash:   l.BlockHash.Hex(),
		LogIndex:    l.Index,
		Removed:     l.Removed,
	}
}

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

	// Grab a pooled connection once for the whole batch.
	pc, err := GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		return fmt.Errorf("LogWriter.Write: failed to get DB connection: %w", err)
	}
	defer PutMainDBConnection(pc)

	for _, l := range logs {
		if l == nil {
			continue
		}

		value, err := json.Marshal(ethLogToEntry(l))
		if err != nil {
			return fmt.Errorf("LogWriter.Write: marshal failed: %w", err)
		}

		// 1. Primary key
		primaryKey := fmt.Sprintf("log:%d:%d:%d", l.BlockNumber, l.TxIndex, l.Index)
		if err := Create(pc, primaryKey, value); err != nil {
			return fmt.Errorf("LogWriter.Write: primary key store failed: %w", err)
		}

		// 2. By-address index
		addrKey := fmt.Sprintf("logaddr:%s:%d:%d", l.Address.Hex(), l.BlockNumber, l.Index)
		if err := Create(pc, addrKey, value); err != nil {
			// Non-fatal — index write; log but continue
			fmt.Printf("LogWriter.Write: addr index store warning: %v\n", err)
		}

		// 3. By-topic index (one entry per topic position)
		for _, topic := range l.Topics {
			topicKey := fmt.Sprintf("logtopic:%s:%d:%d", topic.Hex(), l.BlockNumber, l.Index)
			if err := Create(pc, topicKey, value); err != nil {
				fmt.Printf("LogWriter.Write: topic index store warning: %v\n", err)
			}
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
