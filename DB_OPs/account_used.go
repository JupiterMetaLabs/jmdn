package DB_OPs

import "math/big"

// IsUsedAccount reports whether a has ever held value or sent a transaction:
// a positive balance, or a transaction nonce or sent count above zero.
//
// A row that exists but was never used (for example one written by
// DID.RegisterDID, which the address owner can do for free) is NOT a used
// account. The new-recipient gas rule (config/new_recipient_gas.go) treats
// such a recipient as new, so registering an address first does not avoid the
// cost of creating it. An account drained to zero is still used: draining it
// took a sent transaction, so its nonce is above zero.
//
// A nil account, or a balance that does not parse, is not used.
func IsUsedAccount(a *Account) bool {
	if a == nil {
		return false
	}
	if a.TxNonce > 0 || a.TxCountSent > 0 {
		return true
	}
	if a.Balance == "" {
		return false
	}
	bal, ok := new(big.Int).SetString(a.Balance, 10)
	return ok && bal.Sign() > 0
}
