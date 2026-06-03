// MODULE: DB_OPs/thebegateway/types.go
// PURPOSE: Domain record types — DTOs between JMDN callers and the ThebeDB gateway.
//
// CORE DATA STRUCTURES:
//   - *Record types: plain value structs; fixed size except slices (Payload, BlockNumbers, Metadata)
//   - OutboxEntry: WAL entry; Payload is bounded by JSON-serialized record size (~10KB max)
//
// TO MODIFY BEHAVIOR:
//   - Add field to existing record: add to struct + update apply func in thebeprofile/ + migration
//   - Add new record type: define struct here + add to interfaces.go + implement in thebeprofile/
//
// DO NOT:
//   - Import gossipnode/DB_OPs (cycle risk)
//   - Add business logic to these structs (plain data only)
//
// EXTENSION POINT: new SQL table → new *Record struct here + interface method + apply func

package thebegateway

import "time"

// Namespace identifies which SQL table or KV prefix an OutboxEntry targets.
// OutboxWorker dispatches on Namespace — Method is informational only.
type Namespace string

const (
	NamespaceAccount    Namespace = "account"
	NamespaceBlock      Namespace = "block"
	NamespaceTransaction Namespace = "tx"
	NamespaceSnapshot   Namespace = "snapshot"
	NamespaceZKProof    Namespace = "zk"
	NamespaceL1Finality Namespace = "l1_finality"
)

// MaxOutboxAttempts is the ceiling for OutboxStore retry loops.
// Entries at or above this count are not retried and left for operator inspection.
const MaxOutboxAttempts = 3

// AccountRecord maps to the `accounts` SQL table.
// Mirrors DB_OPs.Account fields without importing that package.
type AccountRecord struct {
	Address     string         `json:"address"`      // CHAR(42) — 0x-prefixed hex
	DIDAddress  string         `json:"did_address"`  // TEXT — W3C DID string
	BalanceWei  string         `json:"balance_wei"`  // VARCHAR(30) — decimal string
	Nonce       string         `json:"nonce"`        // VARCHAR(30) — decimal string
	AccountType int16          `json:"account_type"` // SMALLINT — 0=legacy, 1=publickey
	Metadata    map[string]any `json:"metadata"`     // JSONB
	CreatedAt   time.Time      `json:"created_at"`   // TIMESTAMPTZ
	UpdatedAt   time.Time      `json:"updated_at"`   // TIMESTAMPTZ
}

// BlockRecord maps to the `blocks` SQL table.
// Derived from config.ZKBlock fields.
type BlockRecord struct {
	BlockNumber  uint64         `json:"block_number"`  // BIGINT
	BlockHash    string         `json:"block_hash"`    // CHAR(66) — 0x-prefixed 32-byte hex
	ParentHash   string         `json:"parent_hash"`   // CHAR(66) — maps to ZKBlock.PrevHash
	Timestamp    time.Time      `json:"timestamp"`     // TIMESTAMPTZ
	TxsRoot      string         `json:"txs_root"`      // CHAR(66) — maps to ZKBlock.TxnsRoot
	StateRoot    string         `json:"state_root"`    // CHAR(66)
	LogsBloom    []byte         `json:"logs_bloom"`    // BYTEA
	CoinbaseAddr string         `json:"coinbase_addr"` // CHAR(42)
	ZKVMAddr     string         `json:"zkvm_addr"`     // CHAR(42)
	GasLimit     uint64         `json:"gas_limit"`     // NUMERIC(78,0)
	GasUsed      uint64         `json:"gas_used"`      // NUMERIC(78,0)
	Status       int16          `json:"status"`        // SMALLINT
	ExtraData    map[string]any `json:"extra_data"`    // JSONB
}

// TransactionRecord maps to the `transactions` SQL table.
// Derived from config.Transaction + caller-supplied block context.
type TransactionRecord struct {
	TxHash             string         `json:"tx_hash"`              // CHAR(66)
	BlockNumber        uint64         `json:"block_number"`         // BIGINT
	TxIndex            int16          `json:"tx_index"`             // SMALLINT
	FromAddr           string         `json:"from_addr"`            // CHAR(42)
	ToAddr             *string        `json:"to_addr"`              // CHAR(42) nullable — nil = contract creation
	ValueWei           string         `json:"value_wei"`            // NUMERIC(78,0) as string
	Nonce              string         `json:"nonce"`                // NUMERIC(78,0) as string
	Type               int16          `json:"type"`                 // SMALLINT — 0=Legacy, 1=AccessList, 2=DynamicFee
	GasLimit           string         `json:"gas_limit"`            // VARCHAR(30)
	GasPriceWei        string         `json:"gas_price_wei"`        // VARCHAR(30)
	MaxFeeWei          string         `json:"max_fee_wei"`          // VARCHAR(30)
	MaxPriorityFeeWei  string         `json:"max_priority_fee_wei"` // VARCHAR(30)
	Data               []byte         `json:"data"`                 // BYTEA
	AccessList         map[string]any `json:"access_list"`          // JSONB
	SigV               uint64         `json:"sig_v"`                // BIGINT — int16 overflows for chainID > 16383 (EIP-155)
	SigR               string         `json:"sig_r"`                // CHAR(66)
	SigS               string         `json:"sig_s"`                // CHAR(66)
}

// SnapshotRecord maps to the `snapshots` SQL table.
type SnapshotRecord struct {
	BlockNumber uint64    `json:"block_number"` // BIGINT
	BlockHash   string    `json:"block_hash"`   // CHAR(66)
	CreatedAt   time.Time `json:"created_at"`   // TIMESTAMPTZ — used for LWW conflict resolution on retry
}

// ZKProofRecord maps to the `zk_proofs` SQL table.
// Derived from config.ZKBlock ZK fields.
type ZKProofRecord struct {
	BlockNumber uint64 `json:"block_number"` // BIGINT
	ProofHash   string `json:"proof_hash"`   // CHAR(66)
	StarkProof  []byte `json:"stark_proof"`  // BYTEA
	Commitment  []byte `json:"commitment"`   // BYTEA
}

// L1FinalityRecord maps to the `l1_finality` SQL table.
type L1FinalityRecord struct {
	Confirmation string         `json:"confirmation"`   // CHAR(42)
	BlockNumbers []uint64       `json:"block_numbers"`  // BIGINT[]
	Metadata     map[string]any `json:"metadata"`       // JSONB
}

// OutboxEntry is a WAL entry for failed ThebeGateway writes.
// Retried by OutboxWorker with exponential backoff up to MaxOutboxAttempts.
// OutboxWorker dispatches on Namespace. Method is informational only (logging/debugging).
type OutboxEntry struct {
	ID          int64     `json:"id"`
	Namespace   Namespace `json:"namespace"`     // dispatch key — use Namespace* constants
	Method      string    `json:"method"`        // informational only — ThebeGateway method name for logs
	Payload     []byte    `json:"payload"`       // JSON-serialized domain record
	Attempts    int       `json:"attempts"`
	NextRetryAt time.Time `json:"next_retry_at"`
	CreatedAt   time.Time `json:"created_at"`
}
