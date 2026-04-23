package Types

import (
	"math/big"

	"gossipnode/config"
)

// Withdrawal represents EIP-4895 withdrawal
type Withdrawal struct {
	Index          uint64 `json:"index"`
	ValidatorIndex uint64 `json:"validatorindex"`
	Address        []byte `json:"address"`
	Amount         uint64 `json:"amount"`
}

type Block struct {
	Header          *BlockHeader  `json:"header"`
	Transactions    []*Tx         `json:"transactions"`
	Ommers          [][]byte      `json:"ommers"`
	WithdrawalsRoot []byte        `json:"withdrawalsroot"`
	Withdrawals     []*Withdrawal `json:"withdrawals"`
	BlobGasUsed     []byte        `json:"blobgasused"`
	ExcessBlobGas   []byte        `json:"excessblobgas"`
}

type BlockHeader struct {
	ParentHash          []byte `json:"parenthash"`
	StateRoot           []byte `json:"stateroot"`
	ReceiptsRoot        []byte `json:"receiptsroot"`
	LogsBloom           []byte `json:"logsbloom"`
	Miner               []byte `json:"miner"`
	Number              uint64 `json:"number"`
	GasLimit            uint64 `json:"gaslimit"`
	GasUsed             uint64 `json:"gasused"`
	Timestamp           uint64 `json:"timestamp"`
	MixHashOrPrevRandao []byte `json:"mixhashorprevrandao"` // nil
	BaseFee             []byte `json:"basefee"`
	BlobGasUsedField    uint64 `json:"blobgasusedfield"`   // nil
	ExcessBlobGasField  uint64 `json:"excessblobgasfield"` // nil
	ExtraData           []byte `json:"extradata"`
	Hash                []byte `json:"hash"`
}

type Tx struct {
	Hash                 []byte             `json:"hash"`
	From                 []byte             `json:"from"`
	To                   []byte             `json:"to"`
	Input                []byte             `json:"input"`
	Nonce                uint64             `json:"nonce"`
	Value                []byte             `json:"value"`
	Gas                  uint64             `json:"gas"`
	GasPrice             []byte             `json:"gasprice"`
	Type                 uint32             `json:"type"`
	R                    []byte             `json:"r"`
	S                    []byte             `json:"s"`
	V                    uint32             `json:"v"`
	AccessList           *config.AccessList `json:"accesslist"`
	MaxFeePerGas         []byte             `json:"maxfeepergas"`
	MaxPriorityFeePerGas []byte             `json:"maxpriorityfeepergas"`
	MaxFeePerBlobGas     []byte             `json:"maxfeeperblobgas"`
	BlobVersionedHashes  [][]byte           `json:"blobversionedhashes"`
	BlockNumber          *uint64            `json:"blocknumber,omitempty"`
	BlockHash            []byte             `json:"blockhash,omitempty"`
	TransactionIndex     *uint64            `json:"transactionindex,omitempty"`
}

// Log represents an Ethereum log with complete fields
type Log struct {
	Address     []byte   `json:"address"`
	Topics      [][]byte `json:"topics"`
	Data        []byte   `json:"data"`
	BlockNumber uint64   `json:"blocknumber"`
	BlockHash   []byte   `json:"blockhash"`
	TxIndex     uint64   `json:"txindex"`
	TxHash      []byte   `json:"txhash"`
	LogIndex    uint64   `json:"logindex"`
	Removed     bool     `json:"removed"`
}

type CallMsg struct {
	From, To      string
	Data          []byte
	Value         *big.Int
	Gas, GasPrice *big.Int
}

type FilterQuery struct {
	FromBlock, ToBlock *big.Int
	Addresses          []string
	Topics             [][]string
}
