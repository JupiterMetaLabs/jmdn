// MODULE: DB_OPs/local_create_gate
// PURPOSE: Kill-switch for OUT-OF-BAND (non-block) account creation.
//
// INVARIANT ENFORCED: accounts come into existence ONLY by applying a
// consensus-certified block (BlockProcessing creates receivers from the
// block-carried ART identity; reconciliation replays stored blocks). Any path
// that creates an account outside a block — RPC register-on-read, submit-time
// auto-register, the DID service, DID propagation, operator CLI — creates it
// on SOME nodes only, with a locally minted ART nonce, which is exactly the
// fleet divergence the block-carried identity work removes. All such paths are
// gated here and DISABLED by default.
//
// JMDN_ALLOW_LOCAL_ACCOUNT_CREATE=1 re-enables them (emergency/ops escape
// hatch only; expect AccountSync churn for any account created this way).
package DB_OPs

import (
	"errors"
	"os"
	"strings"
)

// AllowLocalAccountCreate gates every out-of-band account-creation path.
// Default OFF: accounts are created only by block application.
var AllowLocalAccountCreate = envFlagOn("JMDN_ALLOW_LOCAL_ACCOUNT_CREATE", false)

// ErrLocalAccountCreateDisabled is returned by gated creation paths.
var ErrLocalAccountCreateDisabled = errors.New(
	"local account creation is disabled (accounts are created by block application; set JMDN_ALLOW_LOCAL_ACCOUNT_CREATE=1 to override)")

func envFlagOn(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
