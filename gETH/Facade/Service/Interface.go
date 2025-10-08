package Service

import (
	"context"
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

type Service interface {
	ChainID(ctx context.Context) (*big.Int, error)
	ClientVersion(ctx context.Context) (string, error)
	BlockNumber(ctx context.Context) (*big.Int, error)
	BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Block, error)
	Balance(ctx context.Context, addr string, block *big.Int) (*big.Int, error)
	Call(ctx context.Context, msg CallMsg, block *big.Int) ([]byte, error)
	EstimateGas(ctx context.Context, msg CallMsg) (uint64, error)
	GasPrice(ctx context.Context) (*big.Int, error) // or return base+tip separately
	SendRawTx(ctx context.Context, rawHex string) (string, error)
	TxByHash(ctx context.Context, hash string) (*Tx, error)
	ReceiptByHash(ctx context.Context, hash string) (map[string]any, error)
	GetLogs(ctx context.Context, q FilterQuery) ([]Log, error)

	// Streaming (for WS subscriptions)
	SubscribeNewHeads(ctx context.Context) (<-chan *Block, func(), error)
	SubscribeLogs(ctx context.Context, q *FilterQuery) (<-chan Log, func(), error)
	SubscribePendingTxs(ctx context.Context) (<-chan string, func(), error)
}
