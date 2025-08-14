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
    AccessList     AccessList // Now uses the locally defined type     // For EIP-2930 (Type 1) and EIP-1559 (Type 2)
    V, R, S        *big.Int   // Signature values
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
    CoinbaseAddr string               `json:"coinbaseaddr"` // DID of the miner
    ZKVMAddr     string               `json:"zkvmaddr"`     // DID of the ZKVM 
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
    EffectiveGasFee *big.Int // The gas fee that will be charged
}