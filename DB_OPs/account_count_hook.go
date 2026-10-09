package DB_OPs

// Account-creation notification hook.
//
// The explorer stats API needs a fast account/DID count. Rather than scanning
// immudb per request, a counter is maintained in the txindex sqlite: seeded once
// at startup and incremented as new accounts are persisted. DB_OPs cannot call
// the txindex package directly (txindex already imports DB_OPs — the reverse
// would be an import cycle), so the increment is delivered through this injected
// hook, wired from main to txindex.IncrAccountCount.
//
// CONTRACT: the hook MUST be non-blocking (main wires an async applier); it runs
// on the account-write path. It fires only for genuinely NEW accounts (address:
// keys), so its total matches DB_OPs.CountAccounts. It is best-effort — the
// counter can be re-seeded if it ever drifts.
//
// NOTE: fireAccountCreated has no caller yet — main wires the hook
// (SetAccountCreatedHook -> txindex.IncrAccountCount) but the account-write
// path does not fire it, so the counter only ever holds its startup seed.
// Wiring it is a behavioural change tracked separately; the plumbing is kept
// (not deleted) so that fix is a one-line call.
var onAccountCreated func(delta int) //nolint:unused // see NOTE above: hook plumbing awaiting its caller

// SetAccountCreatedHook installs (or clears, with nil) the new-account hook.
// Call once at startup, before account writes begin.
func SetAccountCreatedHook(fn func(delta int)) { onAccountCreated = fn }

// fireAccountCreated notifies the hook that delta brand-new accounts were
// persisted. No-op when delta <= 0 or no hook is wired.
func fireAccountCreated(delta int) { //nolint:unused // see NOTE above: hook plumbing awaiting its caller
	if delta <= 0 {
		return
	}
	if fn := onAccountCreated; fn != nil {
		fn(delta)
	}
}
