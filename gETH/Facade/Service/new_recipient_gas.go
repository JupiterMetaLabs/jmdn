package Service

import (
	"context"

	"github.com/JupiterMetaLabs/ion"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	Utils "gossipnode/gETH/Facade/Service/utils"
)

// New-recipient gas (config/new_recipient_gas.go) in eth_estimateGas.
//
// Lookups are variables so tests can drive every branch without a database.
var (
	estimateChainTip = func(ctx context.Context) (uint64, error) {
		return DB_OPs.GetLatestBlockNumber(ctx, nil)
	}
	// estimateAccountUsed reports whether to is a used account
	// (DB_OPs.IsUsedAccount). A missing account is (false, nil); only a
	// genuine read failure is an error.
	estimateAccountUsed = func(to string) (bool, error) {
		acct, err := DB_OPs.GetAccount(nil, Utils.ConvertAddressCaseInsensitive(to))
		if err == nil {
			return DB_OPs.IsUsedAccount(acct), nil
		}
		if DB_OPs.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
)

// newRecipientExtraGas returns the gas to add to a plain transfer's estimate:
// config.NewRecipientGas when the rule is on and the recipient is not a used
// account, otherwise 0.
//
// It never fails the estimate, and it quotes only what submission on this node
// enforces:
//   - chain tip unreadable: 0. Submission skips the rule in that case
//     (Security.checkSubmissionNewRecipientGas), so quoting the extra gas would
//     overcharge the sender for a rule the node is not applying.
//   - account unreadable: the extra gas. Submission would reject the transfer
//     if the recipient is in fact new, so the estimate errs high; an error here
//     would instead block every plain transfer on this node.
func newRecipientExtraGas(ctx context.Context, to string) uint64 {
	if config.NewRecipientGasFrom() == 0 || to == "" {
		return 0
	}
	tip, err := estimateChainTip(ctx)
	if err != nil {
		logger().Warn(ctx, "eth_estimateGas: chain tip unreadable, new-recipient gas not quoted (submission skips the rule too)",
			ion.String("error", err.Error()))
		return 0
	}
	if !config.NewRecipientGasActiveAt(tip + 1) {
		return 0
	}
	used, err := estimateAccountUsed(to)
	if err != nil {
		logger().Warn(ctx, "eth_estimateGas: recipient account unreadable, assuming a new recipient",
			ion.String("to", to), ion.String("error", err.Error()))
		return config.NewRecipientGas
	}
	if used {
		return 0
	}
	return config.NewRecipientGas
}
