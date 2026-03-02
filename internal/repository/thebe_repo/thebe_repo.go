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

	var accountType int
	if account.AccountType == "did" {
		accountType = 0
	} else {
		accountType = 1
	}

	metadataJSON, err := json.Marshal(account.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}

	data, err := json.Marshal(account)
	if err != nil {
		return fmt.Errorf("thebe_repo.StoreAccount: marshal for KV: %w", err)
	}

	kvKey := []byte(fmt.Sprintf("account:%s", account.Address.Hex()))

	var didAddress *string
	if account.DIDAddress != "" {
		didAddress = &account.DIDAddress
	}

	createdAtSec := account.CreatedAt / 1e9
	if createdAtSec == 0 {
		createdAtSec = time.Now().Unix()
	}

	query := `
		INSERT INTO accounts (address, did_address, balance_wei, nonce, account_type, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, to_timestamp($7), NOW())
		ON CONFLICT (address) DO UPDATE SET 
			balance_wei = EXCLUDED.balance_wei, 
			nonce = EXCLUDED.nonce,
			updated_at = NOW()
	`

	_, err = builder.New(r.db).
		ExecuteKv(builder.KVPutDerived(kvKey, data)).
		ExecuteSQL(query,
			account.Address.Hex(),
			didAddress,
			account.Balance,
			account.Nonce,
			accountType,
			metadataJSON,
			createdAtSec,
		).
		Atomic(ctx, true)

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

	// 1. Write to KV
	updateData := map[string]string{
		"address": address.Hex(),
		"balance": newBalance,
	}
	data, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("thebe_repo.UpdateAccountBalance: marshal for KV: %w", err)
	}

	kvKey := []byte(fmt.Sprintf("account_update:%s:%d", address.Hex(), time.Now().UnixNano()))

	query := `
		INSERT INTO accounts (address, balance_wei, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (address) DO UPDATE SET 
			balance_wei = EXCLUDED.balance_wei,
			updated_at = NOW()
	`

	_, err = builder.New(r.db).
		ExecuteKv(builder.KVPutDerived(kvKey, data)).
		ExecuteSQL(query, address.Hex(), newBalance).
		Atomic(ctx, true)

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
		return fmt.Errorf("thebe_repo.StoreZKBlock: marshal for KV: %w", err)
	}

	kvKey := []byte(fmt.Sprintf("block:%d", block.BlockNumber))
	b := builder.New(r.db)
	b.ExecuteKv(builder.KVPutDerived(kvKey, data))

	extraDataJSON := "{}"
	if block.ExtraData != "" {
		m := map[string]string{"data": block.ExtraData}
		js, _ := json.Marshal(m)
		extraDataJSON = string(js)
	}

	var coinbaseAddr *string
	if block.CoinbaseAddr != nil {
		hex := block.CoinbaseAddr.Hex()
		coinbaseAddr = &hex
	}

	var zkvmAddr *string
	if block.ZKVMAddr != nil {
		hex := block.ZKVMAddr.Hex()
		zkvmAddr = &hex
	}

	statusStr := block.Status
	if statusStr == "" {
		statusStr = "pending"
	}

	// 1. Insert Block Query
	blockQuery := `
		INSERT INTO blocks (
			block_number, block_hash, parent_hash, timestamp, txns_root, state_root, 
			logs_bloom, coinbase_addr, zkvm_addr, gas_limit, gas_used, status, extra_data
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT DO NOTHING
	`
	b.ExecuteSQL(blockQuery,
		block.BlockNumber,
		block.BlockHash.Hex(),
		block.PrevHash.Hex(),
		block.Timestamp,
		block.TxnsRoot,
		block.StateRoot.Hex(),
		block.LogsBloom,
		coinbaseAddr,
		zkvmAddr,
		block.GasLimit,
		block.GasUsed,
		statusStr,
		extraDataJSON,
	)

	// 2. Insert Transactions Queries
	txQuery := `
		INSERT INTO transactions (
			tx_hash, block_number, tx_index, from_addr, to_addr, value_wei, nonce, type, 
			gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei, data, access_list, sig_v, sig_r, sig_s
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT DO NOTHING
	`

	for i, t := range block.Transactions {
		var toAddr *string
		if t.To != nil {
			hex := t.To.Hex()
			toAddr = &hex
		}

		fromAddr := ""
		if t.From != nil {
			fromAddr = t.From.Hex()
		}

		valStr := "0"
		if t.Value != nil {
			valStr = t.Value.String()
		}

		var gasPrice *string
		if t.GasPrice != nil {
			str := t.GasPrice.String()
			gasPrice = &str
		}

		var maxFee *string
		if t.MaxFee != nil {
			str := t.MaxFee.String()
			maxFee = &str
		}

		var maxPriorityFee *string
		if t.MaxPriorityFee != nil {
			str := t.MaxPriorityFee.String()
			maxPriorityFee = &str
		}

		var sigV int
		if t.V != nil {
			sigV = int(t.V.Int64())
		}

		var sigR *string
		if t.R != nil {
			str := "0x" + t.R.Text(16)
			sigR = &str
		}

		var sigS *string
		if t.S != nil {
			str := "0x" + t.S.Text(16)
			sigS = &str
		}

		accessListJSON, txErr := json.Marshal(t.AccessList)
		if txErr != nil || string(accessListJSON) == "null" {
			accessListJSON = []byte("[]")
		}

		b.ExecuteSQL(txQuery,
			t.Hash.Hex(),
			block.BlockNumber,
			i,
			fromAddr,
			toAddr,
			valStr,
			t.Nonce,
			t.Type,
			t.GasLimit,
			gasPrice,
			maxFee,
			maxPriorityFee,
			t.Data,
			accessListJSON,
			sigV,
			sigR,
			sigS,
		)
	}

	// 3. Insert ZK Proof Query (if exists)
	if block.ProofHash != "" && len(block.StarkProof) > 0 {
		zkQuery := `
			INSERT INTO zk_proofs (proof_hash, block_number, stark_proof, commitment)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING
		`
		commitmentJSON, _ := json.Marshal(block.Commitment)

		b.ExecuteSQL(zkQuery,
			block.ProofHash,
			block.BlockNumber,
			block.StarkProof,
			commitmentJSON,
		)
	}

	_, err = b.Atomic(ctx, true)
	if err != nil {
		return fmt.Errorf("thebe_repo.StoreZKBlock: atomic transaction failed: %w", err)
	}

	if span != nil {
		span.SetAttributes(attribute.Float64("duration_ms", float64(time.Since(start).Milliseconds())))
	}

	return nil
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
		return fmt.Errorf("thebe_repo.StoreTransaction: marshal for KV: %w", err)
	}

	kvKey := []byte(fmt.Sprintf("tx:%s", t.Hash.Hex()))

	txQuery := `
		INSERT INTO transactions (
			tx_hash, block_number, tx_index, from_addr, to_addr, value_wei, nonce, type, 
			gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei, data, access_list, sig_v, sig_r, sig_s
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT DO NOTHING
	`

	var toAddr *string
	if t.To != nil {
		hex := t.To.Hex()
		toAddr = &hex
	}

	fromAddr := ""
	if t.From != nil {
		fromAddr = t.From.Hex()
	}

	valStr := "0"
	if t.Value != nil {
		valStr = t.Value.String()
	}

	var gasPrice *string
	if t.GasPrice != nil {
		str := t.GasPrice.String()
		gasPrice = &str
	}

	var maxFee *string
	if t.MaxFee != nil {
		str := t.MaxFee.String()
		maxFee = &str
	}

	var maxPriorityFee *string
	if t.MaxPriorityFee != nil {
		str := t.MaxPriorityFee.String()
		maxPriorityFee = &str
	}

	var sigV int
	if t.V != nil {
		sigV = int(t.V.Int64())
	}

	var sigR *string
	if t.R != nil {
		str := "0x" + t.R.Text(16)
		sigR = &str
	}

	var sigS *string
	if t.S != nil {
		str := "0x" + t.S.Text(16)
		sigS = &str
	}

	accessListJSON, err := json.Marshal(t.AccessList)
	if err != nil || string(accessListJSON) == "null" {
		accessListJSON = []byte("[]")
	}

	_, err = builder.New(r.db).
		ExecuteKv(builder.KVPutDerived(kvKey, data)).
		ExecuteSQL(txQuery,
			t.Hash.Hex(),
			0, // block_number
			0, // tx_index
			fromAddr,
			toAddr,
			valStr,
			t.Nonce,
			t.Type,
			t.GasLimit,
			gasPrice,
			maxFee,
			maxPriorityFee,
			t.Data, // bytes
			accessListJSON,
			sigV,
			sigR,
			sigS,
		).
		Atomic(ctx, true)

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

func (r *ThebeRepository) GetAccount(ctx context.Context, address common.Address) (*DB_OPs.Account, error) {
	query := `SELECT address, did_address, balance_wei, nonce, account_type, metadata, created_at, updated_at
		FROM accounts WHERE address = $1`

	res, err := builder.New(r.db).ExecuteSQL(query, address.Hex()).Atomic(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("thebe_repo.GetAccount: %w", err)
	}

	if len(res.SQL) == 0 {
		return nil, nil // Not found
	}

	row := res.SQL[0]
	addr := row["address"].(string)

	var didAddr string
	if val, ok := row["did_address"].(string); ok {
		didAddr = val
	}

	// Because Postgres numeric often scans as string depending on driver settings in builder, fallback properly:
	var balance string
	if b, ok := row["balance_wei"].([]uint8); ok {
		balance = string(b)
	} else if s, ok := row["balance_wei"].(string); ok {
		balance = s
	}

	nonce := int64(0)
	if val, ok := row["nonce"].(int64); ok {
		nonce = val
	} else if u, ok := row["nonce"].([]uint8); ok {
		fmt.Sscanf(string(u), "%d", &nonce)
	} else if f, ok := row["nonce"].(float64); ok {
		nonce = int64(f)
	}

	acc := &DB_OPs.Account{
		Address:    common.HexToAddress(addr),
		Balance:    balance,
		Nonce:      uint64(nonce),
		DIDAddress: didAddr,
	}

	return acc, nil
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
