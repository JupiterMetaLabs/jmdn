// MODULE: DB_OPs/backend
// PURPOSE: Implement store.ThebeHandle by delegating writes to ThebeGateway and reads to
//          ThebeReader. Zero business logic, zero cache, zero retry — pure delegation.
//
// CORE DATA STRUCTURES:
//   - thebeBackend: zero-state struct; holds two interface deps (gw, r). Stateless per-call.
//     Size: fixed (2 interface fields = 2×2 words = 32 bytes on 64-bit). Growth: none.
//
// TO MODIFY BEHAVIOR:
//   - Change write path: edit thebegateway.ThebeGateway implementation, not this file.
//   - Change read path: edit thebegateway.ThebeReader implementation, not this file.
//   - Add new entity (e.g. SnapshotStore): add methods on thebeBackend, embed new XStore in ThebeHandle.
//
// DO NOT:
//   - Import config.PooledConnection or any ImmuDB-specific types.
//   - Import DB_OPs/dualdb, DB_OPs/immuclient, or DB_OPs/account_immuclient.
//   - Add cache logic here — cache lives in DB_OPs/store/cache/ decorators (Phase 3).
//   - Store any per-request state on thebeBackend (stateless by design).
//
// EXTENSION POINT: implement new store.XxxStore interface → add methods on thebeBackend →
//   embed in ThebeHandle in store/interfaces.go — this file only needs the compile-time assertion.
//
// CHANGE SCENARIOS:
//   Add new entity: add methods on thebeBackend → update var _ assertion → zero other changes here.
//   Swap gateway impl: inject new ThebeGateway at New() call site — this file unchanged.
//   Swap reader impl: inject new ThebeReader at New() call site — this file unchanged.

package backend

import (
	"context"

	"gossipnode/DB_OPs/store"
	"gossipnode/DB_OPs/thebegateway"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// LogWriter persists EVM-emitted logs and retrieves them by filter.
// Implement to swap the storage backend without touching thebeBackend.
type LogWriter interface {
	StoreLogs(ctx context.Context, logs []*ethtypes.Log) error
	GetLogs(ctx context.Context, filter store.LogFilter) ([]*ethtypes.Log, error)
}

// thebeBackend is the concrete implementation of store.ThebeHandle.
// It owns exactly one invariant: implement ThebeHandle by pure delegation.
// No business logic, no retry, no cache.
type thebeBackend struct {
	gw thebegateway.ThebeGateway
	r  thebegateway.ThebeReader
	lw LogWriter
}

// New constructs a thebeBackend. gw and r are required; lw may be nil
// (StoreLogs/GetLogs will return an error if called without a LogWriter).
// gw: write surface (ThebeDB 2PC).
// r: read surface (cache-through SQL/KV).
// lw: log persistence (EVM event log storage).
func New(gw thebegateway.ThebeGateway, r thebegateway.ThebeReader, lw LogWriter) *thebeBackend {
	return &thebeBackend{gw: gw, r: r, lw: lw}
}

// compile-time assertion: thebeBackend must satisfy store.ThebeHandle.
var _ store.ThebeHandle = (*thebeBackend)(nil)

// Close releases no resources — deps manage their own lifecycle.
// Satisfies io.Closer as part of store.ThebeHandle.
func (b *thebeBackend) Close() error { return nil }
