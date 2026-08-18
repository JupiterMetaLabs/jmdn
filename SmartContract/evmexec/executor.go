// Package evmexec implements execbridge.ContractExecutor over the in-tree EVM
// for the consensus apply path (audit EVM-01 wiring, phase P1c).
//
// It is the deterministic bridge the block-apply path calls for contract txs:
//   - state comes from a fresh ContractDB seeded from the LOCAL committed ledger
//     (contractDB.NewContractDBWithAccountSource), never the DID gRPC read
//     (EVM-A16); a state-read error FAILS CLOSED (the tx is aborted);
//   - the block context is fully block-derived (no time.Now / no network),
//     including Random so the VM runs Shanghai, not London (EVM-29);
//   - gas is intrinsic + execution gas via core.IntrinsicGas (EVM-30);
//   - it NEVER writes balances — it returns absolute BalanceChanges for the
//     caller to fold through config.FoldContractExecution under the state-apply
//     lock (P2), so the executor is not a second, divergent balance writer.
//
// UNTESTED-FOR-COMPILE: this file (and everything it imports: go-ethereum/vm,
// core, contractDB) requires a CGO build the sandbox cannot run. It is written
// against APIs pinned via `go doc`. Host gate:
//
//	CGO_ENABLED=1 go build ./SmartContract/evmexec/ ./execbridge/
package evmexec

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	gethcore "github.com/ethereum/go-ethereum/core"

	"gossipnode/DB_OPs/contractDB"
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/config"
	"gossipnode/execbridge"
)

// Executor is the apply-path EVM contract executor. Construct it with New and
// register it via execbridge.SetExecutor when cfg.Contracts.Enabled.
type Executor struct {
	chainID int
	evm     *evm.EVMExecutor

	// newContractDB returns a FRESH apply-path ContractDB per tx, seeded from the
	// local committed ledger (NewContractDBWithAccountSource). Supplied by the
	// node so this package does not hard-depend on the ledger/repo wiring.
	newContractDB func() (*contractDB.ContractDB, error)

	// hasCode reports whether addr holds contract code, over local committed
	// state — deterministic (no network). Used by IsContractTx.
	hasCode func(addr common.Address) bool
}

// New builds an apply-path executor.
func New(chainID int, newContractDB func() (*contractDB.ContractDB, error), hasCode func(common.Address) bool) *Executor {
	return &Executor{
		chainID:       chainID,
		evm:           evm.NewEVMExecutor(chainID),
		newContractDB: newContractDB,
		hasCode:       hasCode,
	}
}

var _ execbridge.ContractExecutor = (*Executor)(nil)

// Register assembles the apply-path executor and installs it as the process-wide
// contract executor. Call once at startup ONLY when cfg.Contracts.Enabled — with
// the flag off, the seam stays on execbridge's no-op and the apply path is
// unchanged (non-breaking). src is the local-ledger balance source (EVM-A16),
// repo the contract-state repository, hasCode the deterministic code-presence
// check (contractDB.HasCode). The repo is reused across txs; a fresh ContractDB
// (fresh in-memory stateObjects) is built per tx.
func Register(chainID int, src contractDB.AccountReader, repo contractDB.StateRepository, hasCode func(common.Address) bool) {
	newDB := func() (*contractDB.ContractDB, error) {
		return contractDB.NewContractDBWithAccountSource(src, repo), nil
	}
	execbridge.SetExecutor(New(chainID, newDB, hasCode))
}

// IsContractTx reports whether tx must go through ExecuteTx: a deployment
// (To == nil) or a call to an address that holds code. Pure + deterministic.
func (e *Executor) IsContractTx(tx *config.Transaction) bool {
	if tx == nil {
		return false
	}
	if tx.To == nil {
		return true // contract creation
	}
	return e.hasCode != nil && e.hasCode(*tx.To)
}

// ExecuteTx runs tx against a fresh local-ledger-backed StateDB and returns its
// effects. It NEVER commits balances; BalanceChanges are absolute post-exec
// balances (value moves only — the VM runs at gas price 0) for the caller to
// fold through config.FoldContractExecution. A reverted tx returns Success=false
// with no balance changes (the caller still charges the flat gas fee).
func (e *Executor) ExecuteTx(_ context.Context, tx *config.Transaction, bctx execbridge.BlockExecContext) (*execbridge.ExecResult, error) {
	if tx == nil {
		return &execbridge.ExecResult{Handled: false}, nil
	}
	cdb, err := e.newContractDB()
	if err != nil {
		return nil, fmt.Errorf("evmexec: build state db: %w", err)
	}
	cdb.SetTxContext(tx.Hash, bctx.BlockNumber)

	var caller common.Address
	if tx.From != nil {
		caller = *tx.From
	}
	value := tx.Value
	if value == nil {
		value = new(big.Int)
	}
	gasLimit := config.EffectiveGasLimit(tx.GasLimit)
	isCreate := tx.To == nil

	det := evm.DetBlockContext{
		BlockNumber: bctx.BlockNumber,
		Time:        bctx.Time,
		Coinbase:    bctx.Coinbase,
		GasLimit:    bctx.GasLimit,
		BaseFee:     nil,            // 0 (flat fee model; fee charged by the caller)
		Random:      bctx.BlockHash, // EVM-29: block-derived PREVRANDAO -> Shanghai
		GetHash:     nil,            // deterministic zero-hash for historical lookups
	}

	var exec *evm.ExecutionResult
	if isCreate {
		exec, err = e.evm.DeployContractWithContext(cdb, det, caller, tx.Data, value, gasLimit)
	} else {
		exec, err = e.evm.ExecuteContractWithContext(cdb, det, caller, *tx.To, tx.Data, value, gasLimit)
	}

	// EVM-A16 fail-closed: a non-deterministic state read aborts the tx BEFORE any
	// effect is reported, so no node commits state derived from a defaulted read.
	if dberr := cdb.DBError(); dberr != nil {
		return &execbridge.ExecResult{
			Handled: true,
			Success: false,
			Err:     fmt.Errorf("evmexec: aborting contract tx %s: deterministic state read failed: %w", tx.Hash.Hex(), dberr),
		}, nil
	}

	// EVM-30: intrinsic gas (Shanghai: Homestead + EIP-2028 + EIP-3860 enabled)
	// plus execution gas. Informational for receipts; the ledger fee is the flat
	// config.GasFee (gasLimit-based) applied by the caller.
	intrinsic, gerr := gethcore.IntrinsicGas(tx.Data, nil, nil, isCreate, true, true, true)
	if gerr != nil {
		return &execbridge.ExecResult{
			Handled: true,
			Success: false,
			Err:     fmt.Errorf("evmexec: intrinsic gas for tx %s: %w", tx.Hash.Hex(), gerr),
		}, nil
	}
	gasUsed := intrinsic
	if exec != nil {
		gasUsed += exec.GasUsed
	}

	res := &execbridge.ExecResult{Handled: true, GasUsed: gasUsed}

	// A reverted / errored execution pays gas but moves no value and commits no
	// contract state.
	if err != nil || exec == nil || exec.Error != nil {
		res.Success = false
		if exec != nil && exec.Error != nil {
			res.Err = exec.Error
		} else if err != nil {
			res.Err = err
		}
		return res, nil
	}

	// Success: commit contract state (deterministic root) and report absolute
	// balance changes for the caller to fold + apply.
	root, commitErr := cdb.CommitToDB(false)
	if commitErr != nil {
		return &execbridge.ExecResult{
			Handled: true,
			Success: false,
			Err:     fmt.Errorf("evmexec: commit contract state for tx %s: %w", tx.Hash.Hex(), commitErr),
		}, nil
	}

	changes := make(map[common.Address]*big.Int)
	for addr, bal := range cdb.GetBalanceChanges() {
		if bal != nil {
			changes[addr] = bal.ToBig()
		}
	}

	res.Success = true
	res.StateRoot = root
	res.BalanceChanges = changes
	if isCreate {
		res.ContractAddress = exec.ContractAddr
	}
	return res, nil
}
