package adapters

import (
	"fmt"

	"github.com/JupiterMetaLabs/avc/interfaces"
)

var _ interfaces.BlockValidator = (*PerBlockValidator)(nil)

// PerBlockValidator implements interfaces.BlockValidator by building a FRESH
// validator for each block — the per-block lifecycle that DepthFull stateful
// checks require.
//
// WHY: stateful (balance/nonce) validation must run against a SecurityCache
// preloaded with exactly THIS block's touched accounts (a bulk ImmuDB read via
// cache.LoadAccounts), and the cache is MUTATED during validation (debit/credit/
// nonce) as the intra-block double-spend guard. A single long-lived validator
// injected once at engine construction cannot provide that — its cache would be
// stale/shared across blocks. So buildForBlock is invoked per ValidateBlock call
// to construct a FullValidator whose cache is loaded for this block (the
// runFullValidatorAgainstDB pattern in shadow.go).
//
// FAIL-CLOSED: if buildForBlock errors or returns nil, ValidateBlock returns a
// non-accept Verdict AND a non-nil error, so the engine's fail-closed
// validateBeforeVote gate vetoes the block (a node that cannot build its
// validator must not vote +1).
type PerBlockValidator struct {
	buildForBlock func(block interfaces.ZKBlock) (interfaces.BlockValidator, error)
}

// NewPerBlockValidator wraps a per-block validator builder. buildForBlock must,
// for the given block: derive the touched account set, load them into a fresh
// SecurityCache, and return a FullValidator (NewStatelessChecker +
// NewStatefulChecker) bound to that cache. jmdn's main.go supplies this closure
// (it needs the DB connection + Security package); it is injected so this
// adapter stays unit-testable without a live ImmuDB.
func NewPerBlockValidator(buildForBlock func(block interfaces.ZKBlock) (interfaces.BlockValidator, error)) (*PerBlockValidator, error) {
	if buildForBlock == nil {
		return nil, fmt.Errorf("adapters.NewPerBlockValidator: nil buildForBlock func")
	}
	return &PerBlockValidator{buildForBlock: buildForBlock}, nil
}

func (v *PerBlockValidator) ValidateBlock(block interfaces.ZKBlock, depth interfaces.ValidationDepth) (interfaces.Verdict, error) {
	fv, err := v.buildForBlock(block)
	if err != nil {
		return interfaces.Rejected(interfaces.ReasonValidatorError,
				"per-block validator build failed: "+err.Error()),
			fmt.Errorf("adapters.PerBlockValidator: build for block: %w", err)
	}
	if fv == nil {
		return interfaces.Rejected(interfaces.ReasonNoValidatorConfig, "per-block validator builder returned nil"),
			fmt.Errorf("adapters.PerBlockValidator: builder returned nil validator")
	}
	return fv.ValidateBlock(block, depth)
}
