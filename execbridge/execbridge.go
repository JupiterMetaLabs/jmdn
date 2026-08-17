// Package execbridge is the adapter seam between block processing (the
// consensus apply path in messaging/BlockProcessing) and a contract-execution
// engine. Today the engine is the in-tree EVM (SmartContract/); the seam is
// deliberately an interface so the INCOMING EXTERNAL AVC MODULE can register
// its own executor without jmdn depending on AVC internals, and vice-versa.
//
// NON-BREAKING BY CONSTRUCTION:
//   - Until an executor is registered, Get() returns a no-op whose IsContractTx
//     is always false, so the apply path behaves EXACTLY as today (contract txs
//     fall through to the existing value path). Registration happens only when
//     the operator sets cfg.Contracts.Enabled — mirroring cfg.Thebe.Enabled and
//     the FastSync flags, which shipped dormant until validated.
//   - The external AVC repo connects by implementing ContractExecutor and
//     calling SetExecutor at its own wiring time. It never edits, and is never
//     broken by, messaging/BlockProcessing.
//
// DETERMINISM CONTRACT (audit EVM-02/09/29/30/31): an executor MUST derive every
// input from BlockExecContext (the agreed block), never from time.Now() or a
// network fetch, and MUST commit deterministically. This interface makes that a
// requirement of the seam, not an implementation detail.
package execbridge

import (
	"context"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
)

// BlockExecContext is the deterministic, block-derived context handed to an
// executor. Every field comes from the agreed block — never wall-clock, never a
// network read — so all nodes compute identical results.
type BlockExecContext struct {
	ChainID     int
	BlockNumber uint64
	BlockHash   common.Hash
	ParentHash  common.Hash
	Time        int64 // block timestamp (unix seconds), NOT wall clock
	Coinbase    common.Address
	TxIndex     int
	GasLimit    uint64
}

// ExecResult is the outcome of executing one contract transaction.
//
// Handled=false means the executor declined (not a contract tx, or execution
// disabled) → the caller applies the tx on the normal value path. This is what
// keeps the seam non-breaking.
//
// BalanceChanges are ABSOLUTE post-execution balances the caller folds back
// through the single centralized account-apply/fee path (config.GasFee /
// SplitFee / the merge writer), so the executor never becomes a second,
// divergent balance writer — the exact class behind the historical divergence
// incident.
type ExecResult struct {
	Handled         bool
	Success         bool
	ContractAddress common.Address              // set on deployment
	GasUsed         uint64                      // fed back through the centralized fee path
	BalanceChanges  map[common.Address]*big.Int // absolute balances, applied by the caller
	StateRoot       common.Hash                 // deterministic post-exec contract-state root (Phase 4)
	Err             error
}

// ContractExecutor executes one contract transaction against durable state,
// deterministically, and returns its effects for the caller to commit through
// the normal atomic account path. Implemented by the EVM bridge today and by
// the external AVC module later.
type ContractExecutor interface {
	// IsContractTx reports whether tx must go through ExecuteTx: a deployment
	// (tx.To == nil) or a call to an address that holds code. Pure and
	// deterministic — no I/O.
	IsContractTx(tx *config.Transaction) bool

	// ExecuteTx runs tx and returns its effects. It MUST NOT perform wall-clock
	// or network I/O; all context comes from bctx. It MUST NOT commit account
	// balances itself — it returns BalanceChanges for the caller to apply under
	// the state-apply lock, atomically with the tx marker.
	ExecuteTx(ctx context.Context, tx *config.Transaction, bctx BlockExecContext) (*ExecResult, error)
}

var (
	mu       sync.RWMutex
	executor ContractExecutor = noop{}
)

// SetExecutor registers the process-wide contract executor. Called once at
// startup, only when contract execution is enabled. A nil argument resets to
// the no-op (execution disabled).
func SetExecutor(e ContractExecutor) {
	mu.Lock()
	defer mu.Unlock()
	if e == nil {
		executor = noop{}
		return
	}
	executor = e
}

// Get returns the registered executor, or a no-op that handles nothing.
func Get() ContractExecutor {
	mu.RLock()
	defer mu.RUnlock()
	return executor
}

// Enabled reports whether a real (non-no-op) executor is registered.
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	_, isNoop := executor.(noop)
	return !isNoop
}

// noop is the default executor: no tx is a contract tx, so the apply path is
// unchanged. This is what makes a merge of the seam non-breaking.
type noop struct{}

func (noop) IsContractTx(*config.Transaction) bool { return false }

func (noop) ExecuteTx(context.Context, *config.Transaction, BlockExecContext) (*ExecResult, error) {
	return &ExecResult{Handled: false}, nil
}
