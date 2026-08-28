package adapters

import (
	"context"
	"fmt"

	"github.com/JupiterMetaLabs/avc/interfaces"

	"gossipnode/Security"
)

// StatefulChecker implements avc's interfaces.StatefulTxChecker by wrapping
// jmdn's EXISTING SecurityCache — it does not reimplement balance or nonce
// logic.
//
// Phase-2 checks, in the exact order and with the exact mutating semantics of
// jmdn's allChecksWithConn (Security.go):
//
//  1. address existence — CheckAddressExistWithCache (sender must exist;
//     receiver must exist unless contract creation)
//  2. balance — CheckBalanceWithCache, which on success DEBITS the sender and
//     CREDITS the receiver in the cache. This mutation is the intra-block
//     double-spend guard: a second tx from the same sender sees the reduced
//     balance.
//  3. nonce — reject tx.Nonce < account.TxNonce, then UpdateTxNonce to
//     tx.Nonce+1. This mirrors jmdn's CURRENT policy EXACTLY, including that it
//     accepts future nonces (tx.Nonce > expected) — see jmdn's TODO(nonce-gap)
//     at Security.go:620. This checker deliberately does not "fix" that; it
//     reproduces jmdn's behaviour so a buddy's verdict matches the producer's.
//
// Because CheckBalanceWithCache and UpdateTxNonce MUTATE the cache, this
// checker MUST be run serially and in block order (avc's
// SerialStatefulValidator does exactly that), and one StatefulChecker instance
// is scoped to ONE block: it holds that block's cache.
//
// # CACHE POPULATION — the caller's responsibility
//
// The SecurityCache must already contain every account this block touches
// before CheckAndApply runs. In production jmdn populates it once per block via
// cache.LoadAccounts(ctx, dbConn, accountsSet) (a bulk ImmuDB read). In tests
// it is populated with cache.RegisterAccount(addr, &DB_OPs.Account{...}) — no
// DB. Either way, StatefulChecker only reads/mutates the in-memory cache; it
// never touches the DB itself, which is what keeps it unit-testable.
type StatefulChecker struct {
	cache *Security.SecurityCache
}

// NewStatefulChecker wraps a pre-populated (or about-to-be-populated) cache.
// A nil cache is refused (fail-closed): a checker with no account state would
// reject or mis-handle every transaction.
func NewStatefulChecker(cache *Security.SecurityCache) (*StatefulChecker, error) {
	if cache == nil {
		return nil, fmt.Errorf("adapters.NewStatefulChecker: nil security cache")
	}
	return &StatefulChecker{cache: cache}, nil
}

// CheckAndApply implements interfaces.StatefulTxChecker. It checks and, on
// success, applies the transaction's effect to the cache (debit/credit/nonce)
// before returning — check and mutate are one step, exactly as jmdn does it.
func (c *StatefulChecker) CheckAndApply(ctx context.Context, tx interfaces.Transaction) error {
	jb, ok := tx.(jmdnBacked)
	if !ok {
		return fmt.Errorf("adapters.StatefulChecker: transaction is not jmdn-backed (%T) — refusing (fail-closed)", tx)
	}
	t := jb.JMDNTransaction()
	if t.From == nil {
		return fmt.Errorf("adapters.StatefulChecker: tx %s has nil From", t.Hash.Hex())
	}

	// 1. address existence
	if ok, err := c.cache.CheckAddressExistWithCache(&t, ctx); !ok || err != nil {
		return fmt.Errorf("adapters.StatefulChecker: address existence failed for tx %s: %v", t.Hash.Hex(), err)
	}

	// 2. balance (mutates the cache: debit sender, credit receiver)
	ok2, err := c.cache.CheckBalanceWithCache(&t, ctx)
	if err != nil {
		return fmt.Errorf("adapters.StatefulChecker: balance check errored for tx %s: %w", t.Hash.Hex(), err)
	}
	if !ok2 {
		// jmdn returns (false, nil) for genuine insufficiency (not an error).
		return fmt.Errorf("adapters.StatefulChecker: insufficient funds for tx %s (sender %s)", t.Hash.Hex(), t.From.Hex())
	}

	// 3. nonce — mirror jmdn's exact policy (reject < expected, then increment).
	account := c.cache.GetAccount(*t.From)
	if account == nil {
		return fmt.Errorf("adapters.StatefulChecker: sender %s not in cache for nonce check", t.From.Hex())
	}
	expectedNonce := account.TxNonce
	if t.Nonce < expectedNonce {
		return fmt.Errorf("adapters.StatefulChecker: nonce too low for %s: got %d, expected >= %d",
			t.From.Hex(), t.Nonce, expectedNonce)
	}
	c.cache.UpdateTxNonce(*t.From, t.Nonce+1)

	return nil
}

// Compile-time assertion.
var _ interfaces.StatefulTxChecker = (*StatefulChecker)(nil)
