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
	// estimateAccountExists reports whether to has an account. A missing
	// account is (false, nil); only a genuine read failure is an error.
	estimateAccountExists = func(to string) (bool, error) {
		_, err := DB_OPs.GetAccount(nil, Utils.ConvertAddressCaseInsensitive(to))
		if err == nil {
			return true, nil
		}
		if DB_OPs.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
)

// newRecipientExtraGas returns the gas to add to a plain transfer's estimate:
// config.NewRecipientGas when the rule is on and the recipient has no account,
// otherwise 0.
//
// It never fails the estimate. When the chain tip or the account cannot be
// read it assumes the rule applies and the recipient is new: the estimate can
// only come out high, which submission accepts, whereas an error would block
// every plain transfer on this node, funded recipients included.
func newRecipientExtraGas(ctx context.Context, to string) uint64 {
	if config.NewRecipientGasFrom() == 0 || to == "" {
		return 0
	}
	if tip, err := estimateChainTip(ctx); err == nil && !config.NewRecipientGasActiveAt(tip+1) {
		return 0
	} else if err != nil {
		logger().Warn(ctx, "eth_estimateGas: chain tip unreadable, assuming the new-recipient rule applies",
			ion.String("error", err.Error()))
	}
	exists, err := estimateAccountExists(to)
	if err != nil {
		logger().Warn(ctx, "eth_estimateGas: recipient account unreadable, assuming a new recipient",
			ion.String("to", to), ion.String("error", err.Error()))
		return config.NewRecipientGas
	}
	if exists {
		return 0
	}
	return config.NewRecipientGas
}
