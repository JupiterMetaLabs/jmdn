package Service

import (
	"context"
	"gossipnode/gETH/Facade/Service/Types"
	"math/big"
)

type Service interface {
	ChainID(ctx context.Context) (*big.Int, error)
	ClientVersion(ctx context.Context) (string, error)
	BlockNumber(ctx context.Context) (*big.Int, error)
	BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Types.Block, error)
	Balance(ctx context.Context, addr string, block *big.Int, network string) (*big.Int, error)
	Call(ctx context.Context, msg Types.CallMsg, block *big.Int) ([]byte, error)
	GetTransactionCount(ctx context.Context, addr string, block string) (*big.Int, error)
	GetTransactionCountFrom(ctx context.Context, addr string, block string) (*big.Int, error)
	EstimateGas(ctx context.Context, msg Types.CallMsg) (uint64, error)
	GasPrice(ctx context.Context) (*big.Int, error) // or return base+tip separately
	SendRawTx(ctx context.Context, rawHex string) (string, error)
	TxByHash(ctx context.Context, hash string) (*Types.Tx, error)
	ReceiptByHash(ctx context.Context, hash string) (map[string]any, error)
	GetLogs(ctx context.Context, q Types.FilterQuery) ([]Types.Log, error)
	GetCode(ctx context.Context, addr string, block *big.Int) (string, error)
	FeeHistory(ctx context.Context, blockCount uint64, newest *big.Int, perc []float64) (map[string]any, error)

	// Streaming (for WS subscriptions)
	SubscribeNewHeads(ctx context.Context) (<-chan *Types.Block, func(), error)
	// SubscribeLogs is used to subscribe to logs - Its used by Smartcontracts so it can be skipped for some time - // Future
	SubscribeLogs(ctx context.Context, q *Types.FilterQuery) (<-chan Types.Log, func(), error)
	// This is to get the pending transactions - It will be implemented once MRE is ready - // Future
	SubscribePendingTxs(ctx context.Context) (<-chan string, func(), error)
}
