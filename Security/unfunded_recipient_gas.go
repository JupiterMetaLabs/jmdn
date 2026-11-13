package Security

import (
	"context"
	"fmt"
	"math/big"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/execbridge"
)

// Unfunded-recipient gas (config/unfunded_recipient_gas.go) at submission:
// AllChecks rejects a transfer to an address holding no JMDT whose gas limit
// is below the required one, so a sender learns at once with the limit it
// needs. Block validation does not check this rule.

// isRuleTransfer reports whether tx is a plain value transfer the rule covers:
// it has a recipient and is not routed to the EVM apply path. Contract
// creation and calls to an address with code are exempt; the predicate is the
// one block apply uses to choose the path (Processing.go).
func isRuleTransfer(tx *config.Transaction) bool {
	if tx == nil || tx.To == nil {
		return false
	}
	return !(execbridge.Enabled() && execbridge.Get().IsContractTx(tx))
}

// cachedBalance returns the recipient's balance from the cache, or nil when
// the cache holds no account for it (an address that does not exist yet).
func cachedBalance(cache *SecurityCache, tx *config.Transaction) (*big.Int, error) {
	acct := cache.GetAccount(*tx.To)
	if acct == nil {
		return nil, nil
	}
	if acct.Balance == "" {
		return new(big.Int), nil
	}
	bal, ok := new(big.Int).SetString(acct.Balance, 10)
	if !ok {
		return nil, fmt.Errorf("invalid balance format for account %s", tx.To.Hex())
	}
	return bal, nil
}

// checkUnfundedRecipientGas applies the rule to tx against the balances in
// cache, without checking activation.
func checkUnfundedRecipientGas(tx *config.Transaction, cache *SecurityCache) error {
	if !isRuleTransfer(tx) {
		return nil
	}
	bal, err := cachedBalance(cache, tx)
	if err != nil {
		return err
	}
	return config.CheckUnfundedRecipientGas(tx.GasLimit, bal)
}

// checkSubmissionUnfundedRecipientGas applies the rule to a submitted
// transaction when the next block falls at or above the activation height.
// If the chain tip cannot be read the rule is applied anyway.
func checkSubmissionUnfundedRecipientGas(ctx context.Context, tx *config.Transaction, cache *SecurityCache) error {
	if config.UnfundedRecipientGasFrom() == 0 {
		return nil
	}
	if tip, err := DB_OPs.GetLatestBlockNumber(ctx, nil); err == nil && !config.UnfundedRecipientGasActiveAt(tip+1) {
		return nil
	}
	return checkUnfundedRecipientGas(tx, cache)
}
