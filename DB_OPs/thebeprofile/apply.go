package thebeprofile

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"
)

// Payload types are the internal KV serialisation format.
// They are JSON-marshalled before db.Append() and unmarshalled inside Apply().
// Field names match PostgreSQL column names exactly.
// NUMERIC(78,0) columns use string to avoid uint256 overflow in Go.
// BYTEA columns use []byte. Nullable columns use pointer types.

type accountPayload struct {
	Address     string          `json:"address"`      // CHAR(42) PK
	DIDAddress  *string         `json:"did_address"`  // TEXT UNIQUE nullable
	BalanceWei  string          `json:"balance_wei"`  // NUMERIC(78,0) as string
	Nonce       string          `json:"nonce"`        // NUMERIC(78,0) as string
	AccountType int16           `json:"account_type"` // SMALLINT 0=EOA 1=Contract
	Metadata    json.RawMessage `json:"metadata"`     // JSONB
	CreatedAt   time.Time       `json:"created_at"`   // TIMESTAMPTZ
}

type blockPayload struct {
	BlockNumber  uint64          `json:"block_number"`  // BIGINT PK
	BlockHash    string          `json:"block_hash"`    // CHAR(66) UNIQUE NOT NULL
	ParentHash   string          `json:"parent_hash"`   // CHAR(66) NOT NULL
	Timestamp    time.Time       `json:"timestamp"`     // TIMESTAMPTZ NOT NULL
	TxsRoot      *string         `json:"txs_root"`      // CHAR(66) nullable
	StateRoot    *string         `json:"state_root"`    // CHAR(66) nullable
	LogsBloom    []byte          `json:"logs_bloom"`    // BYTEA nullable
	CoinbaseAddr *string         `json:"coinbase_addr"` // CHAR(42) nullable
	ZKVMAddr     *string         `json:"zkvm_addr"`     // CHAR(42) nullable
	GasLimit     *string         `json:"gas_limit"`     // NUMERIC(78,0) nullable string
	GasUsed      *string         `json:"gas_used"`      // NUMERIC(78,0) nullable string
	Status       int16           `json:"status"`        // SMALLINT 0=Pending 1=Confirmed 2=Finalized
	ExtraData    json.RawMessage `json:"extra_data"`    // JSONB NOT NULL default {}
}

type txPayload struct {
	TxHash            string          `json:"tx_hash"`              // CHAR(66) PK
	BlockNumber       uint64          `json:"block_number"`         // BIGINT FK->blocks
	TxIndex           int             `json:"tx_index"`             // INT NOT NULL
	FromAddr          string          `json:"from_addr"`            // CHAR(42) FK->accounts
	ToAddr            *string         `json:"to_addr"`              // CHAR(42) nullable (nil=contract creation)
	ValueWei          string          `json:"value_wei"`            // NUMERIC(78,0) string
	Nonce             string          `json:"nonce"`                // NUMERIC(78,0) string
	Type              int16           `json:"type"`                 // SMALLINT 0/1/2
	GasLimit          *string         `json:"gas_limit"`            // NUMERIC(78,0) nullable string
	GasPriceWei       *string         `json:"gas_price_wei"`        // NUMERIC(78,0) nullable string
	MaxFeeWei         *string         `json:"max_fee_wei"`          // NUMERIC(78,0) nullable string
	MaxPriorityFeeWei *string         `json:"max_priority_fee_wei"` // NUMERIC(78,0) nullable string
	Data              []byte          `json:"data"`                 // BYTEA nullable (calldata)
	AccessList        json.RawMessage `json:"access_list"`          // JSONB default []
	SigV              *int            `json:"sig_v"`                // INT nullable
	SigR              *string         `json:"sig_r"`                // CHAR(66) nullable
	SigS              *string         `json:"sig_s"`                // CHAR(66) nullable
}

type zkProofPayload struct {
	ProofHash   string    `json:"proof_hash"`   // CHAR(66) PK
	BlockNumber uint64    `json:"block_number"` // BIGINT FK->blocks UNIQUE
	StarkProof  []byte    `json:"stark_proof"`  // BYTEA NOT NULL
	Commitment  []byte    `json:"commitment"`   // BYTEA nullable
	CreatedAt   time.Time `json:"created_at"`
}

type snapshotPayload struct {
	BlockNumber    uint64    `json:"block_number"`     // BIGINT FK->blocks UNIQUE
	BlockHash      string    `json:"block_hash"`       // CHAR(66) UNIQUE
	PrevSnapshotID *int64    `json:"prev_snapshot_id"` // BIGINT self-FK nullable (nil=genesis)
	CreatedAt      time.Time `json:"created_at"`
}

func applyAccount(tx *sql.Tx, value []byte) error {
	var p accountPayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	_, err := tx.Exec(`
		INSERT INTO accounts
		    (address, did_address, balance_wei, nonce,
		     account_type, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
		ON CONFLICT (address) DO UPDATE SET
		    balance_wei  = EXCLUDED.balance_wei,
		    nonce        = EXCLUDED.nonce,
		    did_address  = EXCLUDED.did_address,
		    account_type = EXCLUDED.account_type,
		    metadata     = EXCLUDED.metadata,
		    updated_at   = NOW()`,
		p.Address, p.DIDAddress, p.BalanceWei, p.Nonce,
		p.AccountType, p.Metadata, p.CreatedAt,
	)
	return err
}

func applyBlock(tx *sql.Tx, value []byte) error {
	var p blockPayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	extra := p.ExtraData
	if extra == nil {
		extra = json.RawMessage(`{}`)
	}
	return insertIgnoreUnique(tx, "m_blk", `
		INSERT INTO blocks
		    (block_number, block_hash, parent_hash, timestamp,
		     txs_root, state_root, logs_bloom,
		     coinbase_addr, zkvm_addr,
		     gas_limit, gas_used, status, extra_data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		p.BlockNumber, p.BlockHash, p.ParentHash, p.Timestamp,
		p.TxsRoot, p.StateRoot, p.LogsBloom,
		p.CoinbaseAddr, p.ZKVMAddr,
		p.GasLimit, p.GasUsed, p.Status, extra,
	)
}

func applyTx(tx *sql.Tx, value []byte) error {
	var p txPayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	al := p.AccessList
	if al == nil {
		al = json.RawMessage(`[]`)
	}
	return insertIgnoreUnique(tx, "m_tx", `
		INSERT INTO transactions
		    (tx_hash, block_number, tx_index, from_addr, to_addr,
		     value_wei, nonce, type,
		     gas_limit, gas_price_wei, max_fee_wei, max_priority_fee_wei,
		     data, access_list, sig_v, sig_r, sig_s)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		p.TxHash, p.BlockNumber, p.TxIndex, p.FromAddr, p.ToAddr,
		p.ValueWei, p.Nonce, p.Type,
		p.GasLimit, p.GasPriceWei, p.MaxFeeWei, p.MaxPriorityFeeWei,
		p.Data, al, p.SigV, p.SigR, p.SigS,
	)
}

func applyZKProof(tx *sql.Tx, value []byte) error {
	var p zkProofPayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	return insertIgnoreUnique(tx, "m_zk", `
		INSERT INTO zk_proofs
		    (proof_hash, block_number, stark_proof, commitment)
		VALUES ($1,$2,$3,$4)`,
		p.ProofHash, p.BlockNumber, p.StarkProof, p.Commitment,
	)
}

func applySnapshot(tx *sql.Tx, value []byte) error {
	var p snapshotPayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	return insertIgnoreUnique(tx, "m_sn", `
		INSERT INTO snapshots
		    (block_number, block_hash, prev_snapshot_id)
		VALUES ($1,$2,$3)`,
		p.BlockNumber, p.BlockHash, p.PrevSnapshotID,
	)
}

// insertIgnoreUnique runs INSERT under a SAVEPOINT so a duplicate key (23505)
// does not abort the whole transaction — required because we cannot use
// ON CONFLICT on tables that have append-only RULEs.
func insertIgnoreUnique(tx *sql.Tx, savepoint, query string, args ...any) error {
	if _, err := tx.Exec("SAVEPOINT " + savepoint); err != nil {
		return err
	}
	_, err := tx.Exec(query, args...)
	if err != nil {
		if _, rbErr := tx.Exec("ROLLBACK TO SAVEPOINT " + savepoint); rbErr != nil {
			return rbErr
		}
		if isUniqueViolation(err) {
			return nil
		}
		return err
	}
	_, err = tx.Exec("RELEASE SAVEPOINT " + savepoint)
	return err
}

// isUniqueViolation reports duplicate-key violations for idempotent inserts.
// We cannot use INSERT ... ON CONFLICT on blocks/transactions/zk_proofs/snapshots
// because those tables have PostgreSQL RULEs (append-only); Postgres rejects
// ON CONFLICT when INSERT or UPDATE rules exist on the table.
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}
