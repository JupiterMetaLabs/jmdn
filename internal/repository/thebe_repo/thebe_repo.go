package thebe_repo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"
	log "gossipnode/logging"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/builder"
	corepb "github.com/JupiterMetaLabs/ThebeDB/proto"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"
	"go.opentelemetry.io/otel/attribute"
)

const tracerNameThebe = "ThebeRepo"

// thebeLogger returns the *ion.Ion instance for ThebeDB repo tracing.
func thebeLogger() *ion.Ion {
	l, err := log.NewAsyncLogger().Get().NamedLogger(log.DBCoordinator, "")
	if err != nil {
		return nil
	}
	return l.GetNamedLogger()
}

// ThebeRepository implements the CoordinatorRepository interface using ThebeDB.
type ThebeRepository struct {
	db *thebedb.ThebeDB
}

// NewThebeRepository creates a new ThebeDB-backed repository adapter.
func NewThebeRepository(db *thebedb.ThebeDB) *ThebeRepository {
	return &ThebeRepository{db: db}
}

// ================================================================
// Account Writes
// ================================================================

func (r *ThebeRepository) StoreAccount(ctx context.Context, account *DB_OPs.Account) error {
	logger := thebeLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameThebe).Start(ctx, "thebe.StoreAccount")
		defer span.End()
		span.SetAttributes(attribute.String("address", account.Address.Hex()))
	}

	start := time.Now()

	data, err := json.Marshal(account)
	if err != nil {
		return fmt.Errorf("thebe_repo.StoreAccount: marshal: %w", err)
	}

	record := &corepb.CanonicalRecord{
		Key:       []byte(fmt.Sprintf("account:%s", account.Address.Hex())),
		Namespace: "jmdt",
		Type:      "account",
		Value:     data,
		Timestamp: uint64(time.Now().UnixNano()),
	}

	b := builder.New(r.db).
		ExecuteKv(builder.KVPutDerived(record.Key, data))

	_, err = b.Run()

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	return err
}

func (r *ThebeRepository) UpdateAccountBalance(ctx context.Context, address common.Address, newBalance string) error {
	logger := thebeLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameThebe).Start(ctx, "thebe.UpdateAccountBalance")
		defer span.End()
		span.SetAttributes(attribute.String("address", address.Hex()))
	}

	start := time.Now()

	// In event sourcing, we just append an update event
	updateData := map[string]string{
		"address": address.Hex(),
		"balance": newBalance,
	}
	data, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("thebe_repo.UpdateAccountBalance: marshal: %w", err)
	}

	record := &corepb.CanonicalRecord{
		Key:       []byte(fmt.Sprintf("account_update:%s:%d", address.Hex(), time.Now().UnixNano())),
		Namespace: "jmdt",
		Type:      "account_update",
		Value:     data,
		Timestamp: uint64(time.Now().UnixNano()),
	}

	b := builder.New(r.db).
		ExecuteKv(builder.KVPutDerived(record.Key, data))

	_, err = b.Run()

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	return err
}

// ================================================================
// Block Writes
// ================================================================

func (r *ThebeRepository) StoreZKBlock(ctx context.Context, block *config.ZKBlock) error {
	logger := thebeLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameThebe).Start(ctx, "thebe.StoreZKBlock")
		defer span.End()
		span.SetAttributes(
			attribute.Int64("block_number", int64(block.BlockNumber)),
			attribute.String("block_hash", block.BlockHash.Hex()),
		)
	}

	start := time.Now()

	data, err := json.Marshal(block)
	if err != nil {
		return fmt.Errorf("thebe_repo.StoreZKBlock: marshal: %w", err)
	}

	record := &corepb.CanonicalRecord{
		Key:       []byte(fmt.Sprintf("block:%d", block.BlockNumber)),
		Namespace: "jmdt",
		Type:      "block",
		Value:     data,
		Timestamp: uint64(time.Now().UnixNano()),
	}

	b := builder.New(r.db).
		ExecuteKv(builder.KVPutDerived(record.Key, data))

	_, err = b.Run()

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
		if err != nil {
			span.RecordError(err)
		}
	}

	return err
}

// ================================================================
// Transaction Writes
// ================================================================

func (r *ThebeRepository) StoreTransaction(ctx context.Context, tx interface{}) error {
	logger := thebeLogger()
	var span ion.Span
	if logger != nil {
		ctx, span = logger.Tracer(tracerNameThebe).Start(ctx, "thebe.StoreTransaction")
		defer span.End()
	}

	start := time.Now()

	t, ok := tx.(*config.Transaction)
	if !ok {
		return fmt.Errorf("thebe_repo.StoreTransaction: unsupported transaction type")
	}

	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("thebe_repo.StoreTransaction: marshal: %w", err)
	}

	record := &corepb.CanonicalRecord{
		Key:       []byte(fmt.Sprintf("tx:%s", t.Hash.Hex())),
		Namespace: "jmdt",
		Type:      "transaction",
		Value:     data,
		Timestamp: uint64(time.Now().UnixNano()),
	}

	b := builder.New(r.db).
		ExecuteKv(builder.KVPutDerived(record.Key, data))

	_, err = b.Run()

	if span != nil {
		span.SetAttributes(
			attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())),
			attribute.String("tx_hash", t.Hash.Hex()),
		)
		if err != nil {
			span.RecordError(err)
		}
	}

	return err
}

// ================================================================
// Read Stubs (writes-only phase — all reads still go through ImmuDB)
// ================================================================

func (r *ThebeRepository) GetAccount(_ context.Context, _ common.Address) (*DB_OPs.Account, error) {
	return nil, nil // Fallback to primary ImmuDB
}

func (r *ThebeRepository) GetAccountByDID(_ context.Context, _ string) (*DB_OPs.Account, error) {
	return nil, nil
}

func (r *ThebeRepository) GetZKBlockByNumber(_ context.Context, _ uint64) (*config.ZKBlock, error) {
	return nil, nil
}

func (r *ThebeRepository) GetZKBlockByHash(_ context.Context, _ string) (*config.ZKBlock, error) {
	return nil, nil
}

func (r *ThebeRepository) GetLatestBlockNumber(_ context.Context) (uint64, error) {
	return 0, nil
}

func (r *ThebeRepository) GetLogs(_ context.Context, _ Types.FilterQuery) ([]Types.Log, error) {
	return nil, nil
}

func (r *ThebeRepository) GetTransactionByHash(_ context.Context, _ string) (*config.Transaction, error) {
	return nil, nil
}
