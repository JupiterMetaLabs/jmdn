package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// ZKBlockTransaction represents a single transaction in a ZK block
type Transaction struct {
	Hash      common.Hash     `json:"hash"`               // 0x-prefixed 32-byte
	From      *common.Address `json:"from"`               // 0x-prefixed 20-byte
	To        *common.Address `json:"to,omitempty"`       // nil => contract creation
	Value     *big.Int        `json:"value"`              // big.Int as hex
	Type      uint8           `json:"type"`               // 0x0=Legacy, 0x1=AccessList, 0x2=DynamicFee
	Timestamp uint64          `json:"timestamp"`          // seconds since epoch (if you keep it)
	ChainID   *big.Int        `json:"chain_id,omitempty"` // present for 2930/1559 (and signed legacy w/155)
	Nonce     uint64          `json:"nonce"`
	GasLimit  uint64          `json:"gas_limit"` //TODO: Make it big int

	// Fee fields (use one set depending on Type)
	GasPrice       *big.Int `json:"gas_price,omitempty"`        // Legacy/EIP-2930
	MaxFee         *big.Int `json:"max_fee,omitempty"`          // 1559: maxFeePerGas
	MaxPriorityFee *big.Int `json:"max_priority_fee,omitempty"` // 1559: maxPriorityFeePerGas

	Data       []byte     `json:"data,omitempty"` // input
	AccessList AccessList `json:"access_list,omitempty"`

	// Signature (present once signed)
	V *big.Int `json:"v,omitempty"`
	R *big.Int `json:"r,omitempty"`
	S *big.Int `json:"s,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for Transaction to properly handle ChainID
func (t *Transaction) UnmarshalJSON(data []byte) error {
	// Create a temporary struct to avoid infinite recursion
	type Alias Transaction
	aux := &struct {
		ChainID interface{} `json:"chain_id,omitempty"` // Accept string or number
		*Alias
	}{
		Alias: (*Alias)(t),
	}

	// First, unmarshal everything normally
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Handle ChainID parsing - it might come as string, number, or bytes
	if aux.ChainID != nil {
		fmt.Printf("DEBUG UnmarshalJSON ChainID: type=%T, value=%v\n", aux.ChainID, aux.ChainID)
		switch v := aux.ChainID.(type) {
		case string:
			// Remove 0x prefix if present and trim whitespace
			chainIDStr := strings.TrimSpace(strings.TrimPrefix(v, "0x"))
			fmt.Printf("DEBUG UnmarshalJSON ChainID (string): %q -> %q\n", v, chainIDStr)
			if chainIDStr == "" {
				t.ChainID = nil
				break
			}
			// Try parsing as decimal first
			chainID := new(big.Int)
			_, ok := chainID.SetString(chainIDStr, 10)
			if !ok {
				// If decimal fails, try hex
				_, ok = chainID.SetString(chainIDStr, 16)
				if !ok {
					fmt.Printf("ERROR UnmarshalJSON ChainID: failed to parse string %q\n", chainIDStr)
					return &json.UnmarshalTypeError{Value: "string", Type: nil, Field: "chain_id"}
				}
			}
			fmt.Printf("DEBUG UnmarshalJSON ChainID (string): parsed as %s\n", chainID.String())
			t.ChainID = chainID
		case float64:
			// JSON numbers are unmarshaled as float64
			fmt.Printf("DEBUG UnmarshalJSON ChainID (float64): %f\n", v)
			t.ChainID = big.NewInt(int64(v))
		case int64:
			fmt.Printf("DEBUG UnmarshalJSON ChainID (int64): %d\n", v)
			t.ChainID = big.NewInt(v)
		case uint64:
			fmt.Printf("DEBUG UnmarshalJSON ChainID (uint64): %d\n", v)
			t.ChainID = new(big.Int).SetUint64(v)
		case []byte:
			// If ChainID comes as bytes (ASCII string), convert to string first
			chainIDStr := strings.TrimSpace(string(v))
			fmt.Printf("DEBUG UnmarshalJSON ChainID ([]byte): hex=%x, ASCII=%q\n", v, chainIDStr)
			// Check if it's a valid numeric string
			chainID := new(big.Int)
			_, ok := chainID.SetString(chainIDStr, 10)
			if !ok {
				// If string parsing fails, try interpreting bytes as big-endian integer
				fmt.Printf("DEBUG UnmarshalJSON ChainID: string parse failed, trying bytes interpretation\n")
				chainID.SetBytes(v)
			}
			fmt.Printf("DEBUG UnmarshalJSON ChainID ([]byte): parsed as %s\n", chainID.String())
			t.ChainID = chainID
		default:
			// Try to convert via string representation
			str := strings.TrimSpace(strings.Trim(fmt.Sprintf("%v", v), `"`))
			fmt.Printf("DEBUG UnmarshalJSON ChainID (default): %T -> %q\n", v, str)
			chainID := new(big.Int)
			_, ok := chainID.SetString(str, 10)
			if !ok {
				_, ok = chainID.SetString(str, 16)
				if !ok {
					fmt.Printf("ERROR UnmarshalJSON ChainID: failed to parse default type %T\n", v)
					return &json.UnmarshalTypeError{Value: fmt.Sprintf("%T", v), Type: nil, Field: "chain_id"}
				}
			}
			fmt.Printf("DEBUG UnmarshalJSON ChainID (default): parsed as %s\n", chainID.String())
			t.ChainID = chainID
		}
	}

	return nil
}

// ZKBlock represents a block processed by the ZKVM with proof
type ZKBlock struct {
	// ZK-Stark proof data
	StarkProof []byte   `json:"starkproof"`
	Commitment []uint32 `json:"commitment"`
	ProofHash  string   `json:"proof_hash"`
	Status     string   `json:"status"`
	TxnsRoot   string   `json:"txnsroot"`

	// Block data
	Transactions []Transaction   `json:"transactions"`
	Timestamp    int64           `json:"timestamp"`
	ExtraData    string          `json:"extradata"`
	StateRoot    common.Hash     `json:"stateroot"`
	LogsBloom    []byte          `json:"logsbloom"`
	CoinbaseAddr *common.Address `json:"coinbaseaddr"`
	ZKVMAddr     *common.Address `json:"zkvmaddr"`
	PrevHash     common.Hash     `json:"prevhash"`
	BlockHash    common.Hash     `json:"blockhash"`
	GasLimit     uint64          `json:"gaslimit"`
	GasUsed      uint64          `json:"gasused"`
	BlockNumber  uint64          `json:"blocknumber"`
}

// ParsedZKTransaction is a helper struct with parsed numeric fields
type ParsedZKTransaction struct {
	Original        *Transaction
	ValueBig        *big.Int
	MaxFeeBig       *big.Int
	EffectiveGasFee *big.Int
}

// Receipt represents the result of a transaction execution
type Receipt struct {
	// Transaction identification
	TxHash           common.Hash `json:"tx_hash"`           // Hash of the transaction
	BlockHash        common.Hash `json:"block_hash"`        // Hash of the block containing this transaction
	BlockNumber      uint64      `json:"block_number"`      // Block number where transaction was included
	TransactionIndex uint64      `json:"transaction_index"` // Index of transaction within the block

	// Transaction execution status
	Status uint64 `json:"status"` // 1 = success, 0 = failure
	Type   uint8  `json:"type"`   // Transaction type (0=Legacy, 1=EIP-2930, 2=EIP-1559)

	// Gas consumption
	GasUsed           uint64 `json:"gas_used"`            // Gas consumed by this transaction
	CumulativeGasUsed uint64 `json:"cumulative_gas_used"` // Total gas used up to this transaction in the block

	// Contract creation (if applicable)
	ContractAddress *common.Address `json:"contract_address,omitempty"` // Address of created contract (if any)

	// Event logs generated by the transaction
	Logs      []Log  `json:"logs"`       // Array of log entries
	LogsBloom []byte `json:"logs_bloom"` // Bloom filter for logs

	// ZK-specific fields
	ZKProof  []byte `json:"zk_proof,omitempty"`  // ZK proof for transaction execution
	ZKStatus string `json:"zk_status,omitempty"` // Status of ZK proof verification
}

// Log represents an event log generated during transaction execution
type Log struct {
	Address     common.Address `json:"address"`      // Address of the contract that generated the log
	Topics      []common.Hash  `json:"topics"`       // Indexed log parameters
	Data        []byte         `json:"data"`         // Non-indexed log data
	BlockNumber uint64         `json:"block_number"` // Block number where log was created
	BlockHash   common.Hash    `json:"block_hash"`   // Hash of the block containing this log
	TxHash      common.Hash    `json:"tx_hash"`      // Hash of the transaction that generated this log
	TxIndex     uint64         `json:"tx_index"`     // Index of transaction within the block
	LogIndex    uint64         `json:"log_index"`    // Index of log within the transaction
	Removed     bool           `json:"removed"`      // True if log was removed due to chain reorganization
}

// BlockMessage is a wrapper for a ZKBlock for network propagation.
// It includes metadata for routing and deduplication.
type BlockMessage struct {
	ID        string            `json:"id"`        // Unique message ID
	Sender    string            `json:"sender"`    // Original sender's peer ID
	Timestamp int64             `json:"timestamp"` // Unix timestamp when message was created
	Nonce     string            `json:"nonce"`     // Unique nonce for CRDT
	Block     *ZKBlock          `json:"block"`     // The ZK block data
	Hops      int               `json:"hops"`      // How many hops this message has made
	Type      string            `json:"type"`      // Type of message
	Data      map[string]string `json:"data"`      // Additional data
}
