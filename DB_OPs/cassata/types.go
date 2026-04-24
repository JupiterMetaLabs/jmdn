package cassata

import (
	"encoding/json"
	"time"
)

// Result types returned to JMDN callers.
// Every field maps 1:1 to a PostgreSQL column.
// NUMERIC(78,0) -> string  (prevents uint256 overflow)
// BYTEA         -> []byte
// TIMESTAMPTZ   -> time.Time
// Nullable cols -> pointer types

type AccountResult struct {
	Address     string          `json:"address"`      // CHAR(42) PK
	DIDAddress  *string         `json:"did_address"`  // TEXT nullable
	BalanceWei  string          `json:"balance_wei"`  // NUMERIC(78,0) string
	Nonce       string          `json:"nonce"`        // NUMERIC(78,0) string
	AccountType int16           `json:"account_type"` // SMALLINT 0=EOA 1=Contract
	Metadata    json.RawMessage `json:"metadata"`     // JSONB
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type BlockResult struct {
	BlockNumber  uint64          `json:"block_number"`
	BlockHash    string          `json:"block_hash"`  // CHAR(66)
	ParentHash   string          `json:"parent_hash"` // CHAR(66)
	Timestamp    time.Time       `json:"timestamp"`
	TxsRoot      *string         `json:"txs_root"`      // CHAR(66) nullable
	StateRoot    *string         `json:"state_root"`    // CHAR(66) nullable
	LogsBloom    []byte          `json:"logs_bloom"`    // BYTEA nullable
	CoinbaseAddr *string         `json:"coinbase_addr"` // CHAR(42) nullable
	ZKVMAddr     *string         `json:"zkvm_addr"`     // CHAR(42) nullable
	GasLimit     *string         `json:"gas_limit"`     // NUMERIC(78,0) nullable string
	GasUsed      *string         `json:"gas_used"`      // NUMERIC(78,0) nullable string
	Status       int16           `json:"status"`        // 0=Pending 1=Confirmed 2=Finalized
	ExtraData    json.RawMessage `json:"extra_data"`    // JSONB
	CreatedAt    time.Time       `json:"created_at"`
}

type TxResult struct {
	TxHash            string          `json:"tx_hash"`
	BlockNumber       uint64          `json:"block_number"`
	TxIndex           int             `json:"tx_index"`
	FromAddr          string          `json:"from_addr"`            // CHAR(42)
	ToAddr            *string         `json:"to_addr"`              // CHAR(42) nil=contract creation
	ValueWei          string          `json:"value_wei"`            // NUMERIC(78,0) string
	Nonce             string          `json:"nonce"`                // NUMERIC(78,0) string
	Type              int16           `json:"type"`                 // 0=Legacy 1=AccessList 2=DynamicFee
	GasLimit          *string         `json:"gas_limit"`            // NUMERIC(78,0) nullable string
	GasPriceWei       *string         `json:"gas_price_wei"`        // type 0/1 only
	MaxFeeWei         *string         `json:"max_fee_wei"`          // type 2 only
	MaxPriorityFeeWei *string         `json:"max_priority_fee_wei"` // type 2 only
	Data              []byte          `json:"data"`                 // BYTEA nullable calldata
	AccessList        json.RawMessage `json:"access_list"`          // JSONB
	SigV              *int            `json:"sig_v"`
	SigR              *string         `json:"sig_r"` // CHAR(66) nullable
	SigS              *string         `json:"sig_s"` // CHAR(66) nullable
	CreatedAt         time.Time       `json:"created_at"`
}

type ZKProofResult struct {
	ProofHash   string    `json:"proof_hash"`   // CHAR(66) PK
	BlockNumber uint64    `json:"block_number"` // UNIQUE - 1 proof per block
	StarkProof  []byte    `json:"stark_proof"`  // BYTEA NOT NULL
	Commitment  []byte    `json:"commitment"`   // BYTEA nullable
	CreatedAt   time.Time `json:"created_at"`
}

type SnapshotResult struct {
	SnapshotID     int64     `json:"snapshot_id"`      // BIGINT auto-generated
	BlockNumber    uint64    `json:"block_number"`     // UNIQUE
	BlockHash      string    `json:"block_hash"`       // CHAR(66) UNIQUE
	PrevSnapshotID *int64    `json:"prev_snapshot_id"` // nil = genesis snapshot
	CreatedAt      time.Time `json:"created_at"`
}

// ── Contract result types ─────────────────────────────────────────
// Every field maps 1:1 to a PostgreSQL column.
// Addresses → CHAR(42), hashes → CHAR(66), binary → []byte

type ContractCodeResult struct {
	Address   string    `json:"address"`   // CHAR(42) PK
	Code      []byte    `json:"code"`      // BYTEA
	CodeHash  string    `json:"code_hash"` // CHAR(66)
	CreatedAt time.Time `json:"created_at"`
}

type ContractStorageResult struct {
	Address   string    `json:"address"`   // CHAR(42)
	SlotHash  string    `json:"slot_hash"`  // CHAR(66)
	ValueHash string    `json:"value_hash"` // CHAR(66) zero hash = empty slot
	UpdatedAt time.Time `json:"updated_at"`
}

type ContractStorageMetaResult struct {
	Address           string    `json:"address"`
	SlotHash          string    `json:"slot_hash"`
	ValueHash         string    `json:"value_hash"`
	LastModifiedBlock uint64    `json:"last_modified_block"`
	LastModifiedTx    string    `json:"last_modified_tx"` // CHAR(66)
	UpdatedAt         time.Time `json:"updated_at"`
}

type ContractNonceResult struct {
	Address   string    `json:"address"` // CHAR(42) PK
	Nonce     string    `json:"nonce"`   // NUMERIC(78,0) string
	UpdatedAt time.Time `json:"updated_at"`
}

type ContractMetaResult struct {
	Address         string          `json:"address"`
	CodeHash        string          `json:"code_hash"`
	CodeSize        uint64          `json:"code_size"`
	DeployerAddress string          `json:"deployer_address"`
	DeploymentTx    string          `json:"deployment_tx"`
	DeploymentBlock uint64          `json:"deployment_block"`
	Raw             json.RawMessage `json:"raw"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type ContractReceiptResult struct {
	TxHash          string          `json:"tx_hash"`          // CHAR(66) PK
	BlockNumber     uint64          `json:"block_number"`
	TxIndex         uint64          `json:"tx_index"`
	Status          int16           `json:"status"` // 1=success 0=fail
	GasUsed         string          `json:"gas_used"` // NUMERIC(78,0) string
	ContractAddress string          `json:"contract_address"` // CHAR(42), empty if not deploy
	RevertReason    string          `json:"revert_reason"`
	Logs            json.RawMessage `json:"logs"`
	Raw             []byte          `json:"raw"` // full receipt bytes
	CreatedAt       time.Time       `json:"created_at"`
}
