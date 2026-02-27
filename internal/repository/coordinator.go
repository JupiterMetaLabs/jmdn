package repository

import (
	"context"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	"gossipnode/gETH/Facade/Service/Types"
	"time"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"go.opentelemetry.io/otel/attribute"
)

// MasterRepository implements the CoordinatorRepository interface.
// It coordinates writes across SQL, KV, and ImmuDB repositories.
//
// Write strategy:
//   - ImmuDB (primary) — written synchronously. If it fails, return error.
//   - Postgres + PebbleDB (secondary) — written asynchronously via GRO-tracked
//     goroutines after ImmuDB succeeds. Failures are logged but never block the caller.
//
// Read strategy: try fastest source first (KV), then fall back to ImmuDB.
type MasterRepository struct {
	Thebe CoordinatorRepository
	Immu  CoordinatorRepository
	gro   interfaces.LocalGoroutineManagerInterface
}

// NewMasterRepository creates a new MasterRepository.
// gro is the GRO local manager for tracking secondary write goroutines.
// If gro is nil, secondary writes will use untracked goroutines as a fallback.
func NewMasterRepository(thebe, immu CoordinatorRepository, gro interfaces.LocalGoroutineManagerInterface) *MasterRepository {
	return &MasterRepository{
		Thebe: thebe,
		Immu:  immu,
		gro:   gro,
	}
}

// secondaryWriteTimeout is the maximum time a secondary (async) write is
// allowed to run before the context is cancelled. This prevents goroutines
// from hanging indefinitely if a backend is unresponsive.
const secondaryWriteTimeout = 30 * time.Second

// writeSecondary fires a write to a secondary backend in a GRO-tracked goroutine.
// Each goroutine gets its own timeout context that is cancelled when the write
// completes or times out.
//
// We use context.WithoutCancel(parentCtx) so the async goroutine inherits the
// parent trace (creating true child spans) without inheriting the parent's
// cancellation, allowing the async write to outlive the synchronous HTTP request.
// Errors are logged but never returned to the caller.
func (m *MasterRepository) writeSecondary(parentCtx context.Context, backend string, operation string, fn func(ctx context.Context) error) {
	// Create a new context that is completely detached from parentCtx cancellation,
	// but still retains all context values (like OpenTelemetry trace and span IDs).
	detachedCtx := context.WithoutCancel(parentCtx)

	doWrite := func() {
		ctx, cancel := context.WithTimeout(detachedCtx, secondaryWriteTimeout)
		defer cancel()

		logger := repoLogger()
		if logger != nil {
			var span ion.Span
			// Because ctx contains the parent span context, this creates a CHILD span
			ctx, span = logger.Tracer(tracerName).Start(ctx, "secondary."+operation)
			defer span.End()
			span.SetAttributes(
				attribute.String("backend", backend),
				attribute.String("operation", operation),
			)

			start := time.Now()
			if err := fn(ctx); err != nil {
				span.RecordError(err)
				span.SetAttributes(attribute.String("status", "error"))
				logger.Error(ctx, "secondary write failed",
					err,
					ion.String("backend", backend),
					ion.String("operation", operation),
					ion.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
					ion.String("function", "MasterRepository.writeSecondary"))
			} else {
				span.SetAttributes(
					attribute.String("status", "success"),
					attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
				)
			}
			return
		}

		// Fallback: no logger available — still execute the write
		_ = fn(ctx)
	}

	if m.gro != nil {
		m.gro.Go(GRO.DB_OPsCoordinatorWriteThread, func(_ context.Context) error {
			doWrite()
			return nil // always nil — GRO should never treat this as fatal
		})
	} else {
		go doWrite()
	}
}

// ==========================================
// Account Repository Implementation
// ==========================================

func (m *MasterRepository) StoreAccount(ctx context.Context, account *DB_OPs.Account) error {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.StoreAccount")
		defer span.End()
		span.SetAttributes(attribute.String("address", account.Address.Hex()))
	}

	start := time.Now()

	// 1. Primary — ImmuDB must succeed
	if m.Immu != nil {
		if err := m.Immu.StoreAccount(ctx, account); err != nil {
			if span != nil {
				span.RecordError(err)
				span.SetAttributes(
					attribute.String("status", "primary_failed"),
					attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
				)
			}
			return err
		}
	}

	// 2. Secondary — async fire-and-forget via GRO
	if m.Thebe != nil {
		m.writeSecondary(ctx, "thebe", "StoreAccount", func(ctx context.Context) error {
			return m.Thebe.StoreAccount(ctx, account)
		})
	}

	if span != nil {
		span.SetAttributes(
			attribute.String("status", "success"),
			attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
		)
	}

	return nil
}

func (m *MasterRepository) GetAccount(ctx context.Context, address common.Address) (*DB_OPs.Account, error) {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.GetAccount")
		defer span.End()
		span.SetAttributes(attribute.String("address", address.Hex()))
	}

	if m.Thebe != nil {
		if acc, err := m.Thebe.GetAccount(ctx, address); err == nil && acc != nil {
			if span != nil {
				span.SetAttributes(attribute.String("read_source", "thebe_hit"))
			}
			return acc, nil
		}
	}
	if m.Immu != nil {
		acc, err := m.Immu.GetAccount(ctx, address)
		if span != nil {
			span.SetAttributes(attribute.String("read_source", "immu_fallback"))
		}
		return acc, err
	}
	return nil, nil
}

func (m *MasterRepository) GetAccountByDID(ctx context.Context, did string) (*DB_OPs.Account, error) {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.GetAccountByDID")
		defer span.End()
	}

	if m.Immu != nil {
		return m.Immu.GetAccountByDID(ctx, did)
	}
	_ = span // used for defer
	return nil, nil
}

func (m *MasterRepository) UpdateAccountBalance(ctx context.Context, address common.Address, newBalance string) error {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.UpdateAccountBalance")
		defer span.End()
		span.SetAttributes(attribute.String("address", address.Hex()))
	}

	start := time.Now()

	// 1. Primary
	if m.Immu != nil {
		if err := m.Immu.UpdateAccountBalance(ctx, address, newBalance); err != nil {
			if span != nil {
				span.RecordError(err)
				span.SetAttributes(
					attribute.String("status", "primary_failed"),
					attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
				)
			}
			return err
		}
	}

	// 2. Secondary — async
	if m.Thebe != nil {
		m.writeSecondary(ctx, "thebe", "UpdateAccountBalance", func(ctx context.Context) error {
			return m.Thebe.UpdateAccountBalance(ctx, address, newBalance)
		})
	}

	if span != nil {
		span.SetAttributes(
			attribute.String("status", "success"),
			attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
		)
	}

	return nil
}

// ==========================================
// Block Repository Implementation
// ==========================================

func (m *MasterRepository) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.StoreZKBlock")
		defer span.End()
		span.SetAttributes(
			attribute.Int64("block_number", int64(block.BlockNumber)),
			attribute.String("block_hash", block.BlockHash.Hex()),
			attribute.Int("tx_count", len(block.Transactions)),
		)
	}

	start := time.Now()

	// 1. Primary
	if m.Immu != nil {
		if err := m.Immu.StoreZKBlock(ctx, block); err != nil {
			if span != nil {
				span.RecordError(err)
				span.SetAttributes(
					attribute.String("status", "primary_failed"),
					attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
				)
			}
			return err
		}
	}

	// 2. Secondary — async
	if m.Thebe != nil {
		m.writeSecondary(ctx, "thebe", "StoreZKBlock", func(ctx context.Context) error {
			return m.Thebe.StoreZKBlock(ctx, block)
		})
	}

	if span != nil {
		span.SetAttributes(
			attribute.String("status", "success"),
			attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
		)
	}

	return nil
}

func (m *MasterRepository) GetZKBlockByNumber(ctx context.Context, number uint64) (*config.ZKBlock, error) {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.GetZKBlockByNumber")
		defer span.End()
		span.SetAttributes(attribute.Int64("block_number", int64(number)))
	}

	if m.Thebe != nil {
		if b, err := m.Thebe.GetZKBlockByNumber(ctx, number); err == nil && b != nil {
			if span != nil {
				span.SetAttributes(attribute.String("read_source", "thebe_hit"))
			}
			return b, nil
		}
	}
	if m.Immu != nil {
		b, err := m.Immu.GetZKBlockByNumber(ctx, number)
		if span != nil {
			span.SetAttributes(attribute.String("read_source", "immu_fallback"))
		}
		return b, err
	}
	return nil, nil
}

func (m *MasterRepository) GetZKBlockByHash(ctx context.Context, hash string) (*config.ZKBlock, error) {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.GetZKBlockByHash")
		defer span.End()
		span.SetAttributes(attribute.String("block_hash", hash))
	}

	if m.Thebe != nil {
		if b, err := m.Thebe.GetZKBlockByHash(ctx, hash); err == nil && b != nil {
			if span != nil {
				span.SetAttributes(attribute.String("read_source", "thebe_hit"))
			}
			return b, nil
		}
	}
	if m.Immu != nil {
		b, err := m.Immu.GetZKBlockByHash(ctx, hash)
		if span != nil {
			span.SetAttributes(attribute.String("read_source", "immu_fallback"))
		}
		return b, err
	}
	return nil, nil
}

func (m *MasterRepository) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.GetLatestBlockNumber")
		defer span.End()
	}

	if m.Thebe != nil {
		if max, err := m.Thebe.GetLatestBlockNumber(ctx); err == nil && max > 0 {
			if span != nil {
				span.SetAttributes(
					attribute.String("read_source", "thebe_hit"),
					attribute.Int64("block_number", int64(max)),
				)
			}
			return max, nil
		}
	}
	if m.Immu != nil {
		num, err := m.Immu.GetLatestBlockNumber(ctx)
		if span != nil {
			span.SetAttributes(
				attribute.String("read_source", "immu_fallback"),
				attribute.Int64("block_number", int64(num)),
			)
		}
		return num, err
	}
	return 0, nil
}

func (m *MasterRepository) GetLogs(ctx context.Context, filterQuery Types.FilterQuery) ([]Types.Log, error) {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.GetLogs")
		defer span.End()
	}

	if m.Thebe != nil {
		if logs, err := m.Thebe.GetLogs(ctx, filterQuery); err == nil {
			if span != nil {
				span.SetAttributes(
					attribute.String("read_source", "thebe_hit"),
					attribute.Int("log_count", len(logs)),
				)
			}
			return logs, nil
		}
	}
	if m.Immu != nil {
		logs, err := m.Immu.GetLogs(ctx, filterQuery)
		if span != nil {
			span.SetAttributes(attribute.String("read_source", "immu_fallback"))
		}
		return logs, err
	}
	return nil, nil
}

// ==========================================
// Transaction Repository Implementation
// ==========================================

func (m *MasterRepository) StoreTransaction(ctx context.Context, tx interface{}) error {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.StoreTransaction")
		defer span.End()
	}

	start := time.Now()

	// 1. Primary
	if m.Immu != nil {
		if err := m.Immu.StoreTransaction(ctx, tx); err != nil {
			if span != nil {
				span.RecordError(err)
				span.SetAttributes(
					attribute.String("status", "primary_failed"),
					attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
				)
			}
			return err
		}
	}

	// 2. Secondary — async
	if m.Thebe != nil {
		m.writeSecondary(ctx, "thebe", "StoreTransaction", func(ctx context.Context) error {
			return m.Thebe.StoreTransaction(ctx, tx)
		})
	}

	if span != nil {
		span.SetAttributes(
			attribute.String("status", "success"),
			attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
		)
	}

	return nil
}

func (m *MasterRepository) GetTransactionByHash(ctx context.Context, hash string) (*config.Transaction, error) {
	logger := repoLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerName).Start(ctx, "DB.GetTransactionByHash")
		defer span.End()
		span.SetAttributes(attribute.String("tx_hash", hash))
	}

	if m.Thebe != nil {
		if tx, err := m.Thebe.GetTransactionByHash(ctx, hash); err == nil && tx != nil {
			if span != nil {
				span.SetAttributes(attribute.String("read_source", "thebe_hit"))
			}
			return tx, nil
		}
	}
	if m.Immu != nil {
		tx, err := m.Immu.GetTransactionByHash(ctx, hash)
		if span != nil {
			span.SetAttributes(attribute.String("read_source", "immu_fallback"))
		}
		return tx, err
	}
	return nil, nil
}
