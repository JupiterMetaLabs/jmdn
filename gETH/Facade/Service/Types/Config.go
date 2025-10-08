package Types

import (
	"math/big"
)

type Block struct {
	Number       *big.Int
	Hash         string
	ParentHash   string
	Timestamp    uint64
	Transactions []Tx
}

type Tx struct {
	Hash          string
	From, To      string
	Input         []byte
	Value         *big.Int
	Nonce         uint64
	Gas, GasPrice *big.Int
}

type Log struct {
	Address     string
	Topics      []string
	Data        []byte
	BlockNumber *big.Int
	TxHash      string
	LogIndex    uint32
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