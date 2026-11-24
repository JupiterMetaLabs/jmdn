package BlockProcessing

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
	"gossipnode/execbridge"
)

// PredictContractCreatedAccounts is the SEQUENCER-side, pre-consensus prediction
// of every account a block's contract transactions touch DURING EVM execution:
// value recipients of internal calls (CALL{value}), SELFDESTRUCT beneficiaries,
// value-funded CREATE/CREATE2 children, and deployed contracts. The result is fed
// to DB_OPs.EnrichBlockAccountNoncesWithPredicted so each of those accounts that
// does not exist yet receives a canonical, block-carried ART ordinal BEFORE the
// committee votes. Validators only READ AccountNonces at apply, so blocks
// stamped this way are accepted unchanged by validators on older builds.
//
// Each contract tx is executed with the SAME context helper the apply path uses
// (contractExecContext) against the node's committed state. ExecuteTx commits
// nothing: contract state is behind ExecResult.CommitState, which is never
// called here, and balances are only returned, never written.
//
// LIMITATION (deliberate): each tx is simulated against the PRE-BLOCK state, not
// on top of the earlier txs in the same block. A tx whose execution depends on an
// earlier tx of the same block can therefore touch an address this prediction
// did not see; the apply path then applies that tx as a deterministic revert
// (EVMUnstampedAccountRevertHeight) instead of failing the block.
//
// The returned addresses are an over-approximation input to enrichment, which
// assigns ordinals only to the ones absent from the store; existing accounts are
// stamped with their stored identity exactly as for top-level senders/receivers.
//
// Errors: a non-nil error means a deterministic state read failed during
// simulation (fail closed: the caller rejects the proposal and the orchestrator
// retries). An EVM revert is NOT an error — a reverted tx moves no value.
func PredictContractCreatedAccounts(ctx context.Context, block *config.ZKBlock) ([]common.Address, error) {
	if block == nil || !execbridge.Enabled() {
		return nil, nil
	}
	if block.CoinbaseAddr == nil {
		return nil, fmt.Errorf("predict created accounts: block %d has no coinbase", block.BlockNumber)
	}
	ex := execbridge.Get()
	seen := make(map[common.Address]struct{})
	var out []common.Address
	add := func(a common.Address) {
		if a == (common.Address{}) {
			return
		}
		if _, ok := seen[a]; ok {
			return
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	for i := range block.Transactions {
		tx := block.Transactions[i]
		if !ex.IsContractTx(&tx) {
			continue
		}
		res, err := ex.ExecuteTx(ctx, &tx, contractExecContext(block.BlockNumber, block.BlockHash, block.Timestamp, *block.CoinbaseAddr, i))
		if err != nil {
			return nil, fmt.Errorf("predict created accounts: simulate tx %s (index %d): %w", tx.Hash.Hex(), i, err)
		}
		if res == nil || !res.Handled || !res.Success {
			continue
		}
		for _, a := range sortedAddrs(res.BalanceChanges) {
			add(a)
		}
		if tx.To == nil {
			add(res.ContractAddress)
		}
	}
	return out, nil
}
