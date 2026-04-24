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

// ── Contract payload types ────────────────────────────────────────

type contractCodePayload struct {
	Address  string `json:"address"`   // CHAR(42)
	Code     []byte `json:"code"`      // BYTEA
	CodeHash string `json:"code_hash"` // CHAR(66)
}

type contractStoragePayload struct {
	Address   string `json:"address"`    // CHAR(42)
	SlotHash  string `json:"slot_hash"`  // CHAR(66)
	ValueHash string `json:"value_hash"` // CHAR(66) — zero hash when slot deleted
}

type contractStorageMetaPayload struct {
	Address           string `json:"address"`
	SlotHash          string `json:"slot_hash"`
	ValueHash         string `json:"value_hash"`
	LastModifiedBlock uint64 `json:"last_modified_block"`
	LastModifiedTx    string `json:"last_modified_tx"` // CHAR(66)
}

type contractNoncePayload struct {
	Address string `json:"address"` // CHAR(42)
	Nonce   string `json:"nonce"` // NUMERIC(78,0) string (matches cassata JSON)
}

type contractMetaPayload struct {
	Address         string          `json:"address"`
	CodeHash        string          `json:"code_hash"`
	CodeSize        uint64          `json:"code_size"`
	DeployerAddress string          `json:"deployer_address"`
	DeploymentTx    string          `json:"deployment_tx"`
	DeploymentBlock uint64          `json:"deployment_block"`
	Raw             json.RawMessage `json:"raw"` // full ContractMetadata JSON
}

type contractReceiptPayload struct {
	TxHash          string          `json:"tx_hash"`
	BlockNumber     uint64          `json:"block_number"`
	TxIndex         uint64          `json:"tx_index"`
	Status          int16           `json:"status"`           // 1=success 0=fail
	GasUsed         string          `json:"gas_used"`         // NUMERIC(78,0) string
	ContractAddress string          `json:"contract_address"` // CHAR(42), empty if not deploy
	RevertReason    string          `json:"revert_reason"`
	Logs            json.RawMessage `json:"logs"`
	Raw             []byte          `json:"raw"` // full serialised receipt bytes
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

// ── Contract apply functions ─────────────────────────────────────

func applyContractCode(tx *sql.Tx, value []byte) error {
	var p contractCodePayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	_, err := tx.Exec(`
		INSERT INTO contract_code (address, code, code_hash)
		VALUES ($1,$2,$3)
		ON CONFLICT DO NOTHING`,
		p.Address, p.Code, p.CodeHash,
	)
	return err
}

func applyContractStorage(tx *sql.Tx, value []byte) error {
	var p contractStoragePayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	_, err := tx.Exec(`
		INSERT INTO contract_storage (address, slot_hash, value_hash)
		VALUES ($1,$2,$3)
		ON CONFLICT (address, slot_hash) DO UPDATE SET
			value_hash = EXCLUDED.value_hash,
			updated_at = NOW()`,
		p.Address, p.SlotHash, p.ValueHash,
	)
	return err
}

func applyContractStorageMeta(tx *sql.Tx, value []byte) error {
	var p contractStorageMetaPayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	_, err := tx.Exec(`
		INSERT INTO contract_storage_metadata
			(address, slot_hash, value_hash, last_modified_block, last_modified_tx)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (address, slot_hash) DO UPDATE SET
			value_hash          = EXCLUDED.value_hash,
			last_modified_block = EXCLUDED.last_modified_block,
			last_modified_tx    = EXCLUDED.last_modified_tx,
			updated_at          = NOW()`,
		p.Address, p.SlotHash, p.ValueHash, p.LastModifiedBlock, p.LastModifiedTx,
	)
	return err
}

func applyContractNonce(tx *sql.Tx, value []byte) error {
	var p contractNoncePayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	_, err := tx.Exec(`
		INSERT INTO contract_nonces (address, nonce)
		VALUES ($1,$2)
		ON CONFLICT (address) DO UPDATE SET
			nonce      = EXCLUDED.nonce,
			updated_at = NOW()`,
		p.Address, p.Nonce,
	)
	return err
}

func applyContractMeta(tx *sql.Tx, value []byte) error {
	var p contractMetaPayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	raw := p.Raw
	if raw == nil {
		raw = json.RawMessage(`{}`)
	}
	_, err := tx.Exec(`
		INSERT INTO contract_metadata
			(address, code_hash, code_size, deployer_address,
			 deployment_tx, deployment_block, raw)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (address) DO UPDATE SET
			code_hash        = EXCLUDED.code_hash,
			code_size        = EXCLUDED.code_size,
			deployer_address = EXCLUDED.deployer_address,
			deployment_tx    = EXCLUDED.deployment_tx,
			deployment_block = EXCLUDED.deployment_block,
			raw              = EXCLUDED.raw,
			updated_at       = NOW()`,
		p.Address, p.CodeHash, p.CodeSize, p.DeployerAddress,
		p.DeploymentTx, p.DeploymentBlock, raw,
	)
	return err
}

func applyContractReceipt(tx *sql.Tx, value []byte) error {
	var p contractReceiptPayload
	if err := json.Unmarshal(value, &p); err != nil {
		return err
	}
	logs := p.Logs
	if logs == nil {
		logs = json.RawMessage(`[]`)
	}
	_, err := tx.Exec(`
		INSERT INTO contract_receipts
			(tx_hash, block_number, tx_index, status, gas_used,
			 contract_address, revert_reason, logs, raw)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT DO NOTHING`,
		p.TxHash, p.BlockNumber, p.TxIndex, p.Status, p.GasUsed,
		p.ContractAddress, p.RevertReason, logs, p.Raw,
	)
	return err
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
