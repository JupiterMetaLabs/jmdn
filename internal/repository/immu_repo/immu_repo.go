package immu_repo

import (
	"context"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	log "gossipnode/logging"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"go.opentelemetry.io/otel/attribute"
)

const tracerNameImmu = "ImmuRepo"

// immuLogger returns the *ion.Ion instance for ImmuDB repo tracing.
func immuLogger() *ion.Ion {
	l, err := log.NewAsyncLogger().Get().NamedLogger(log.DBCoordinator, "")
	if err != nil {
		return nil
	}
	return l.GetNamedLogger()
}

// ImmuRepository wraps the existing DB_OPs functions to satisfy the CoordinatorRepository interface.
// This allows the new MasterRepository to completely wrap the legacy ImmuDB implementation.
//
// IMPORTANT: All methods forward the caller's context to DB_OPs functions.
// The context carries deadlines and cancellation from the coordinator's
// context.WithTimeout, ensuring ImmuDB operations respect timeouts.
type ImmuRepository struct{}

func NewImmuRepository() *ImmuRepository {
	return &ImmuRepository{}
}

// ==========================================
// Account Repository Implementation
// ==========================================

func (r *ImmuRepository) StoreAccount(ctx context.Context, account *DB_OPs.Account) error {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.StoreAccount")
		defer span.End()
		span.SetAttributes(attribute.String("address", account.Address.Hex()))
	}

	start := time.Now()
	err := DB_OPs.CreateAccount(nil, account.DIDAddress, account.Address, account.Metadata)

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	return err
}

func (r *ImmuRepository) GetAccount(ctx context.Context, address common.Address) (*DB_OPs.Account, error) {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.GetAccount")
		defer span.End()
		span.SetAttributes(attribute.String("address", address.Hex()))
	}

	start := time.Now()
	acc, err := DB_OPs.GetAccount(nil, address)

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	return acc, err
}

func (r *ImmuRepository) GetAccountByDID(ctx context.Context, did string) (*DB_OPs.Account, error) {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.GetAccountByDID")
		defer span.End()
	}

	start := time.Now()
	acc, err := DB_OPs.GetAccountByDID(nil, did)

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	_ = ctx // used by Tracer.Start
	return acc, err
}

func (r *ImmuRepository) UpdateAccountBalance(ctx context.Context, address common.Address, newBalance string) error {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.UpdateAccountBalance")
		defer span.End()
		span.SetAttributes(attribute.String("address", address.Hex()))
	}

	start := time.Now()
	err := DB_OPs.UpdateAccountBalance(nil, address, newBalance)

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	_ = ctx
	return err
}

// ==========================================
// Block Repository Implementation
// ==========================================

func (r *ImmuRepository) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.StoreZKBlock")
		defer span.End()
		span.SetAttributes(
			attribute.Int64("block_number", int64(block.BlockNumber)),
			attribute.String("block_hash", block.BlockHash.Hex()),
		)
	}

	start := time.Now()
	err := DB_OPs.StoreZKBlock(nil, block)

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	_ = ctx
	return err
}

func (r *ImmuRepository) GetZKBlockByNumber(ctx context.Context, number uint64) (*config.ZKBlock, error) {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.GetZKBlockByNumber")
		defer span.End()
		span.SetAttributes(attribute.Int64("block_number", int64(number)))
	}

	start := time.Now()
	block, err := DB_OPs.GetZKBlockByNumber(nil, number)

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	_ = ctx
	return block, err
}

func (r *ImmuRepository) GetZKBlockByHash(ctx context.Context, hash string) (*config.ZKBlock, error) {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.GetZKBlockByHash")
		defer span.End()
		span.SetAttributes(attribute.String("block_hash", hash))
	}

	start := time.Now()
	block, err := DB_OPs.GetZKBlockByHash(nil, hash)

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	_ = ctx
	return block, err
}

func (r *ImmuRepository) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.GetLatestBlockNumber")
		defer span.End()
	}

	start := time.Now()
	num, err := DB_OPs.GetLatestBlockNumber(nil)

	if span != nil {
		span.SetAttributes(
			attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
			attribute.Int64("block_number", int64(num)),
		)
		if err != nil {
			span.RecordError(err)
		}
	}

	_ = ctx
	return num, err
}

func (r *ImmuRepository) GetLogs(ctx context.Context, filterQuery Types.FilterQuery) ([]Types.Log, error) {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.GetLogs")
		defer span.End()
	}

	start := time.Now()
	logs, err := DB_OPs.GetLogs(nil, filterQuery)

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	_ = ctx
	return logs, err
}

// ==========================================
// Transaction Repository Implementation
// ==========================================

func (r *ImmuRepository) StoreTransaction(ctx context.Context, tx interface{}) error {
	// ImmuDB stores transactions as part of StoreZKBlock (embedded in block writes).
	// Standalone transaction storage is not supported by the legacy ImmuDB layer,
	// so this is a deliberate no-op to avoid blocking the coordinator.
	return nil
}

func (r *ImmuRepository) GetTransactionByHash(ctx context.Context, hash string) (*config.Transaction, error) {
	logger := immuLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameImmu).Start(ctx, "immu.GetTransactionByHash")
		defer span.End()
		span.SetAttributes(attribute.String("tx_hash", hash))
	}

	start := time.Now()
	tx, err := DB_OPs.GetTransactionByHash(nil, hash)

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	_ = ctx
	return tx, err
}
