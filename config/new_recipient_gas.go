package config

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// New-recipient gas (consensus.new_recipient_gas_from_height).
//
// A plain value transfer to a NEW recipient must carry a gas limit of at least
// TransferGas + NewRecipientGas. That transfer is what brings the account into
// use, so it pays for it, as Ethereum's G_newaccount did.
//
// "New" means no USED account (DB_OPs.IsUsedAccount): no row at all, or a row
// that has never held value or sent a transaction, such as one written by a
// free DID registration. Registering an address first therefore does not avoid
// the cost. An account drained to zero is used (draining it took a sent
// transaction) and pays nothing extra.
//
// Enforced at submission (Security.AllChecks) and reflected in eth_estimateGas
// only; block validation does not check it. The fee formula is unchanged:
// GasFee is still gasLimit × effective price, so the higher limit IS the
// higher fee.
//
// Contract creations and calls to an address with code are exempt: the EVM
// path prices them.

const (
	// TransferGas is the intrinsic gas of a plain value transfer. Defined as
	// FallbackTxGasLimit so the two cannot drift apart.
	TransferGas = FallbackTxGasLimit
	// NewRecipientGas is the extra gas a transfer to an address with no
	// account must carry.
	NewRecipientGas uint64 = 25_000
)

// ErrNewRecipientGas matches (errors.Is) every NewRecipientGasError.
var ErrNewRecipientGas = errors.New("gas limit below the minimum for a new recipient")

// NewRecipientGasError reports a transfer to a new recipient whose gas limit
// is below the required one.
type NewRecipientGasError struct {
	Have uint64 // effective gas limit of the transaction
	Need uint64 // required gas limit
}

func (e *NewRecipientGasError) Error() string {
	return fmt.Sprintf("gas limit %d below %d required for a new recipient", e.Have, e.Need)
}

// Is makes errors.Is(err, ErrNewRecipientGas) true.
func (e *NewRecipientGasError) Is(target error) bool { return target == ErrNewRecipientGas }

var newRecipientGasFrom atomic.Uint64

// SetNewRecipientGasFrom sets consensus.new_recipient_gas_from_height; 0 keeps
// the rule off. Called once by main after settings load.
func SetNewRecipientGasFrom(height uint64) { newRecipientGasFrom.Store(height) }

// NewRecipientGasFrom returns the activation height, 0 when off.
func NewRecipientGasFrom() uint64 { return newRecipientGasFrom.Load() }

// NewRecipientGasActiveAt reports whether the rule applies to a block at
// height.
func NewRecipientGasActiveAt(height uint64) bool {
	from := newRecipientGasFrom.Load()
	return from > 0 && height >= from
}

// RequiredTransferGas returns the minimum gas limit for a plain transfer to a
// recipient that is (recipientUsed) or is not a used account. It does not
// check activation; callers do.
func RequiredTransferGas(recipientUsed bool) uint64 {
	if recipientUsed {
		return TransferGas
	}
	return TransferGas + NewRecipientGas
}

// CheckNewRecipientGas returns a *NewRecipientGasError when a plain transfer
// with gasLimit (0 meaning FallbackTxGasLimit, as GasFee charges it) is below
// the limit RequiredTransferGas gives. It does not check activation or whether
// the transfer is a contract transaction; callers do.
func CheckNewRecipientGas(gasLimit uint64, recipientUsed bool) error {
	need := RequiredTransferGas(recipientUsed)
	if have := EffectiveGasLimit(gasLimit); have < need {
		return &NewRecipientGasError{Have: have, Need: need}
	}
	return nil
}
