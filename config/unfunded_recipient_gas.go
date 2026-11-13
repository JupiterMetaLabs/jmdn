package config

import (
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"
)

// Unfunded-recipient gas (consensus.unfunded_recipient_gas_from_height).
//
// A plain value transfer to a recipient that holds no JMDT (balance 0, which
// includes an address with no account yet) must carry a gas limit of at least
// TransferGas + UnfundedRecipientGas. Such a transfer is what creates account
// state, so it pays for it, as Ethereum's G_newaccount did.
//
// Enforced at submission (Security.AllChecks) and reflected in eth_estimateGas
// only; block validation does not check it. The fee formula is unchanged:
// GasFee is still gasLimit × effective price, so the higher limit IS the
// higher fee.
//
// Contract creations and calls to an address with code are exempt: the EVM
// path prices them.

const (
	// TransferGas is the intrinsic gas of a plain value transfer.
	TransferGas uint64 = 21_000
	// UnfundedRecipientGas is the extra gas a transfer to an unfunded
	// recipient must carry.
	UnfundedRecipientGas uint64 = 25_000
)

// ErrUnfundedRecipientGas matches (errors.Is) every UnfundedRecipientGasError.
var ErrUnfundedRecipientGas = errors.New("gas limit below the minimum for an unfunded recipient")

// UnfundedRecipientGasError reports a transfer to an unfunded recipient whose
// gas limit is below the required one.
type UnfundedRecipientGasError struct {
	Have uint64 // effective gas limit of the transaction
	Need uint64 // required gas limit
}

func (e *UnfundedRecipientGasError) Error() string {
	return fmt.Sprintf("gas limit %d below %d required for an unfunded recipient", e.Have, e.Need)
}

// Is makes errors.Is(err, ErrUnfundedRecipientGas) true.
func (e *UnfundedRecipientGasError) Is(target error) bool { return target == ErrUnfundedRecipientGas }

var unfundedRecipientGasFrom atomic.Uint64

// SetUnfundedRecipientGasFrom sets consensus.unfunded_recipient_gas_from_height;
// 0 keeps the rule off. Called once by main after settings load.
func SetUnfundedRecipientGasFrom(height uint64) { unfundedRecipientGasFrom.Store(height) }

// UnfundedRecipientGasFrom returns the activation height, 0 when off.
func UnfundedRecipientGasFrom() uint64 { return unfundedRecipientGasFrom.Load() }

// UnfundedRecipientGasActiveAt reports whether the rule applies to a block at
// height.
func UnfundedRecipientGasActiveAt(height uint64) bool {
	from := unfundedRecipientGasFrom.Load()
	return from > 0 && height >= from
}

// RequiredTransferGas returns the minimum gas limit for a plain transfer whose
// recipient has recipientBalance (nil means no account). It does not check
// activation; callers do.
func RequiredTransferGas(recipientBalance *big.Int) uint64 {
	if recipientBalance == nil || recipientBalance.Sign() <= 0 {
		return TransferGas + UnfundedRecipientGas
	}
	return TransferGas
}

// CheckUnfundedRecipientGas returns an *UnfundedRecipientGasError when a plain
// transfer with gasLimit (0 meaning FallbackTxGasLimit, as GasFee charges it)
// to a recipient with recipientBalance is below the required limit. It does not
// check activation or whether the transfer is a contract transaction; callers
// do.
func CheckUnfundedRecipientGas(gasLimit uint64, recipientBalance *big.Int) error {
	need := RequiredTransferGas(recipientBalance)
	if have := EffectiveGasLimit(gasLimit); have < need {
		return &UnfundedRecipientGasError{Have: have, Need: need}
	}
	return nil
}
