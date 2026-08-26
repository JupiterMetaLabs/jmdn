package Service

import (
	"context"
	"encoding/json"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/txstatus"
	"math/big"
)

type Service interface {
	ChainID(ctx context.Context) (*big.Int, error)
	GetChainIDValue() *big.Int
	ClientVersion(ctx context.Context) (string, error)
	Accounts(ctx context.Context) ([]string, error)
	BlockNumber(ctx context.Context) (*big.Int, error)
	BlockByNumber(ctx context.Context, num *big.Int, fullTx bool) (*Types.Block, error)
	Balance(ctx context.Context, addr string, block *big.Int, network string) (*big.Int, error)
	Call(ctx context.Context, msg Types.CallMsg, block *big.Int) ([]byte, error)
	GetTransactionCount(ctx context.Context, addr string, block string) (*big.Int, error)
	EstimateGas(ctx context.Context, msg Types.CallMsg) (uint64, error)
	GasPrice(ctx context.Context) (*big.Int, error) // or return base+tip separately
	SendRawTx(ctx context.Context, rawHex string) (string, error)
	// TxByHash returns a transaction by hash.
	//
	// When tx_status.pending_tx_by_hash is enabled it may also answer from the
	// mempool, returning a transaction with nil BlockNumber/BlockHash/
	// TransactionIndex — the standard Ethereum pending representation. In that
	// mode a hash that is not known anywhere returns (nil, nil), which the RPC
	// layer serialises as null per spec. With the flag off, behaviour is
	// unchanged: a hash that is not in a block yields an error.
	TxByHash(ctx context.Context, hash string) (*Types.Tx, error)

	// ReceiptByHash returns a transaction receipt, or (nil, nil) when the
	// transaction is not yet mined.
	//
	// This MUST stay null for anything not in a block, and no status feature
	// changes that. Wallets and client libraries treat a non-null receipt as
	// proof of mining and read its status field, so a synthesised receipt
	// carrying status 0x0 would render a merely-queued transaction as FAILED,
	// and one carrying a fabricated block number is worse. Rich pending state
	// belongs on jmdt_getTransactionStatus, not here.
	ReceiptByHash(ctx context.Context, hash string) (map[string]any, error)

	// TxStatus resolves where a transaction is: mined, queued in the mempool,
	// in flight, failed, or unknown. Returns txstatus.ErrDisabled when the
	// feature is off, so a caller can tell that apart from a negative answer.
	TxStatus(ctx context.Context, hash string) (*txstatus.Result, error)

	// PendingTxByHash returns a queued mempool transaction with no block
	// fields, or (nil, nil) when the hash is not queued or the feature is off.
	PendingTxByHash(ctx context.Context, hash string) (*Types.Tx, error)
	GetLogs(ctx context.Context, q Types.FilterQuery) ([]Types.Log, error)
	GetCode(ctx context.Context, addr string, block *big.Int) (string, error)
	FeeHistory(ctx context.Context, blockCount uint64, newest *big.Int, perc []float64) (map[string]any, error)

	// LatestL1CommitBlock returns the most recent block that has L1 commit data
	// (L1TxHash != ""). Scans backwards from the chain tip up to 10 000 blocks.
	// Returns nil, nil if no committed block is found within that window.
	LatestL1CommitBlock(ctx context.Context) (*Types.Block, error)

	// Streaming (for WS subscriptions)
	GetStorageAt(ctx context.Context, address string, slot string, blockNum string) (string, error)
	GetGasPrice(ctx context.Context) (string, error)
	GetFeeHistory(ctx context.Context, blockCount int, newestBlock string, rewardPercentiles []float64) (interface{}, error)
	GetMaxPriorityFeePerGas(ctx context.Context) (string, error)
	IsListening(ctx context.Context) (bool, error)
	GetPeerCount(ctx context.Context) (string, error)
	SubscribeNewHeads(ctx context.Context) (<-chan *Types.Block, func(), error)
	// SubscribeLogs is used to subscribe to logs - Its used by Smartcontracts so it can be skipped for some time - // Future
	SubscribeLogs(ctx context.Context, q *Types.FilterQuery) (<-chan Types.Log, func(), error)
	// This is to get the pending transactions - It will be implemented once MRE is ready - // Future
	SubscribePendingTxs(ctx context.Context) (<-chan string, func(), error)

	// Solidity Compiler
	CompileSolidity(ctx context.Context, source string, optimize bool, runs uint32) (*SolcCompileResult, error)

	// TxPoolContent returns all pending transactions grouped by sender and nonce,
	// in the standard txpool_content JSON-RPC format.
	TxPoolContent(ctx context.Context) (map[string]any, error)

	// debug_traceTransaction — re-executes the transaction with a StructLogger.
	// Returns the raw JSON payload from StructLogger.GetResult() so it can be
	// forwarded verbatim to the caller in the standard Geth debug format.
	// NOTE: best-effort against current state; historical pre-state is Phase 5.
	TraceTransaction(ctx context.Context, txHash string) (json.RawMessage, error)
}

// SolcCompileResult holds compilation results for JSON-RPC
type SolcCompileResult struct {
	ABI              string   `json:"abi"`
	Bytecode         string   `json:"bytecode"`
	DeployedBytecode string   `json:"deployedBytecode"`
	Errors           []string `json:"errors"`
	Warnings         []string `json:"warnings"`
}
