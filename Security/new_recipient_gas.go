package Security

import (
	"context"

	"github.com/JupiterMetaLabs/ion"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/execbridge"
	"gossipnode/metrics"
)

// New-recipient gas (config/new_recipient_gas.go) at submission: AllChecks
// rejects a transfer to an address with no account whose gas limit is below
// the required one, so a sender learns at once with the limit it needs.
// Block validation does not check this rule.

// chainTip returns the latest stored block number. A variable so tests can
// drive the activation gate without a database.
var chainTip = func(ctx context.Context) (uint64, error) {
	return DB_OPs.GetLatestBlockNumber(ctx, nil)
}

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

// checkNewRecipientGas applies the rule to tx against the accounts in cache,
// without checking activation. LoadAccounts has already loaded the recipient
// (fail-closed on a read error), so an absent cache entry means no account.
// A row that was registered but never used counts as new
// (DB_OPs.IsUsedAccount).
func checkNewRecipientGas(tx *config.Transaction, cache *SecurityCache) error {
	if !isRuleTransfer(tx) {
		return nil
	}
	used := DB_OPs.IsUsedAccount(cache.GetAccount(*tx.To))
	return config.CheckNewRecipientGas(tx.GasLimit, used)
}

// checkSubmissionNewRecipientGas applies the rule to a submitted transaction
// when the next block falls at or above the activation height.
//
// If the chain tip cannot be read the rule is SKIPPED: it is this node's
// submission policy, not a consensus rule, so skipping it costs nothing at
// block level, while applying it before activation would reject ordinary
// transfers with an error that reads as the sender's mistake.
func checkSubmissionNewRecipientGas(ctx context.Context, tx *config.Transaction, cache *SecurityCache) error {
	if config.NewRecipientGasFrom() == 0 {
		return nil
	}
	tip, err := chainTip(ctx)
	if err != nil {
		logger().Warn(ctx, "New-recipient gas check skipped: chain tip unreadable",
			ion.String("error", err.Error()),
			ion.String("function", "Security.checkSubmissionNewRecipientGas"))
		return nil
	}
	if !config.NewRecipientGasActiveAt(tip + 1) {
		return nil
	}
	if err := checkNewRecipientGas(tx, cache); err != nil {
		metrics.RPCQueryRefusedCounter.WithLabelValues("eth_sendRawTransaction", "new_recipient_gas").Inc()
		return err
	}
	return nil
}
