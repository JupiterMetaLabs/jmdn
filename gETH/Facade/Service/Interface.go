package Service

import (
	"context"
	"math/big"
	"gossipnode/gETH/Facade/Service/Types"
)

type Service interface {
	ChainID(ctx context.Context) (*big.Int, error)
	ClientVersion(ctx context.Context) (string, error)
	BlockNumber(ctx context.Context) (*big.Int, error)
	BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Types.Block, error)
	Balance(ctx context.Context, addr string, block *big.Int) (*big.Int, error)
	Call(ctx context.Context, msg Types.CallMsg, block *big.Int) ([]byte, error)
	EstimateGas(ctx context.Context, msg Types.CallMsg) (uint64, error)
	GasPrice(ctx context.Context) (*big.Int, error) // or return base+tip separately
	SendRawTx(ctx context.Context, rawHex string) (string, error)
	TxByHash(ctx context.Context, hash string) (*Types.Tx, error)
	ReceiptByHash(ctx context.Context, hash string) (map[string]any, error)
	GetLogs(ctx context.Context, q Types.FilterQuery) ([]Types.Log, error)

	// Streaming (for WS subscriptions)
	SubscribeNewHeads(ctx context.Context) (<-chan *Types.Block, func(), error)
	SubscribeLogs(ctx context.Context, q *Types.FilterQuery) (<-chan Types.Log, func(), error)
	SubscribePendingTxs(ctx context.Context) (<-chan string, func(), error)
}
