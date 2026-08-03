package backend

// MODULE: DB_OPs/backend/log.go
// PURPOSE: Implement store.LogStore by delegating to the LogWriter interface.
// CORE DATA STRUCTURES: []*ethtypes.Log passed through; no internal storage on thebeBackend.
// TO MODIFY BEHAVIOR: swap the LogWriter implementation passed to New()
// DO NOT: import ImmuDB, PooledConnection, or dualdb packages
// EXTENSION POINT: implement LogWriter with ThebeDB SQL backing to replace ImmuDB log storage

import (
	"context"
	"fmt"

	"gossipnode/DB_OPs/store"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// StoreLogs persists EVM-emitted logs via the injected LogWriter.
// Time: O(n) where n = len(logs).
func (b *thebeBackend) StoreLogs(ctx context.Context, logs []*ethtypes.Log) error {
	if b.lw == nil {
		return fmt.Errorf("backend.StoreLogs: LogWriter not configured")
	}
	if err := b.lw.StoreLogs(ctx, logs); err != nil {
		return fmt.Errorf("backend.StoreLogs: %w", err)
	}
	return nil
}

// GetLogs retrieves logs matching the filter via the injected LogWriter.
// Time: O(n) where n = number of log entries matching the filter.
func (b *thebeBackend) GetLogs(ctx context.Context, filter store.LogFilter) ([]*ethtypes.Log, error) {
	if b.lw == nil {
		return nil, fmt.Errorf("backend.GetLogs: LogWriter not configured")
	}
	logs, err := b.lw.GetLogs(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("backend.GetLogs: %w", err)
	}
	return logs, nil
}
