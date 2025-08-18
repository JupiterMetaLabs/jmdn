package config

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// ZKBlockTransaction represents a single transaction in a ZK block
type ZKBlockTransaction struct {
    Hash           string `json:"hash"`
    From           string `json:"from"`
    To             string `json:"to"`
    Value          string `json:"value"`
    Type           string `json:"type"`
    Timestamp      string `json:"timestamp"`
    ChainID        string `json:"chain_id"`
    Nonce          string `json:"nonce"`
    GasLimit       string `json:"gas_limit"`
    MaxFee        string `json:"max_fee,omitempty"`
    MaxPriorityFee string `json:"max_priority_fee,omitempty"`
    Data           string `json:"data"`
    GasPrice       string `json:"gas_price,omitempty"`
    AccessList     AccessList `json:"access_list,omitempty"`
    V string `json:"v,omitempty"`
    R string `json:"r,omitempty"`
    S string `json:"s,omitempty"`
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
    Transactions []ZKBlockTransaction `json:"transactions"`
    Timestamp    int64                `json:"timestamp"`
    ExtraData    string               `json:"extradata"`
    StateRoot    common.Hash          `json:"stateroot"`
    LogsBloom    []byte               `json:"logsbloom"`
    CoinbaseAddr string               `json:"coinbaseaddr"`
    ZKVMAddr     string               `json:"zkvmaddr"`    
    PrevHash     common.Hash          `json:"prevhash"`
    BlockHash    common.Hash          `json:"blockhash"`
    GasLimit     uint64               `json:"gaslimit"`
    GasUsed      uint64               `json:"gasused"`
    BlockNumber  uint64               `json:"blocknumber"`
}

// ParsedZKTransaction is a helper struct with parsed numeric fields
type ParsedZKTransaction struct {
    Original       *ZKBlockTransaction
    ValueBig       *big.Int
    MaxFeeBig      *big.Int
    EffectiveGasFee *big.Int
}

// BlockMessage is a wrapper for a ZKBlock for network propagation.
// It includes metadata for routing and deduplication.
type BlockMessage struct {
	ID        string   `json:"id"`        // Unique message ID
	Sender    string   `json:"sender"`    // Original sender's peer ID
	Timestamp int64    `json:"timestamp"` // Unix timestamp when message was created
	Nonce     string   `json:"nonce"`     // Unique nonce for CRDT
	Block     *ZKBlock `json:"block"`     // The ZK block data
	Hops      int      `json:"hops"`      // How many hops this message has made
    Type      string   `json:"type"`      // Type of message
    Data      map[string]string `json:"data"`      // Additional data
}
