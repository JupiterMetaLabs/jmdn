package adapters

import (
	"context"
	"fmt"
	"math/big"

	"github.com/JupiterMetaLabs/avc/interfaces"
	"github.com/JupiterMetaLabs/avc/validation"
	"github.com/JupiterMetaLabs/ion"

	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/config/settings"
)

// runFullValidatorFn is a package-level function variable, not a plain call to
// runFullValidatorAgainstDB, SPECIFICALLY so tests can override it without a
// live ImmuDB connection. EvaluateShadow's gate/mode logic is fully unit
// tested against a fake; runFullValidatorAgainstDB itself (the real DB read
// path) is NOT exercised by any test in this repo yet — see its doc comment.
var runFullValidatorFn = runFullValidatorAgainstDB

// EvaluateShadow is the single entry point Vote/Trigger.go calls right after
// Security.CheckZKBlockValidation. It NEVER changes behavior for a node that
// hasn't explicitly opted in: cfg nil, Features.AvcValidation.Enabled=false,
// or Network.Environment != "testnet" all return (legacyAccept, legacyErr)
// completely unchanged, byte-for-byte the same as if this function did not
// exist. This is the fail-safe rollout gate agreed for the A3 wiring:
// gradual, per-node (via yaml), testnet-only, shadow before enforce.
//
//   - Enabled=false (default): no-op, returns legacy unchanged.
//   - Enabled=true, Environment != "testnet": refused, logs a warning once per
//     call (cheap; this is per-block, not per-tx), returns legacy unchanged.
//   - Enabled=true, Environment == "testnet", Mode != "enforce" ("shadow",
//     empty, or unrecognized): runs the new FullValidator, logs
//     AVC_SHADOW_MISMATCH if it disagrees with (or the new path errors
//     against) the legacy decision, but the RETURNED decision is still legacy
//     unchanged — this mode can never affect a real vote.
//   - Enabled=true, Environment == "testnet", Mode == "enforce": the new
//     FullValidator's verdict BECOMES the returned decision. An internal error
//     building or running the new path in this mode fails CLOSED (rejects
//     the block) rather than silently falling back to legacy — enforce mode
//     means the new path is the source of truth, so a broken new path must
//     not be masked by a quiet fallback.
func EvaluateShadow(ctx context.Context, cfg *settings.NodeConfig, zkBlock *config.ZKBlock, legacyAccept bool, legacyErr error) (bool, error) {
	if cfg == nil || !cfg.Features.AvcValidation.Enabled {
		return legacyAccept, legacyErr
	}

	if cfg.Network.Environment != "testnet" {
		if l := logger(); l != nil {
			l.Warn(ctx, "AVC_TESTNET_GATE_REFUSED: avc_validation.enabled=true but network.environment is not \"testnet\" — staying on legacy validation only",
				ion.String("environment", cfg.Network.Environment))
		}
		return legacyAccept, legacyErr
	}

	avcAccept, avcErr := runFullValidatorFn(ctx, cfg, zkBlock)

	blockHash := ""
	var blockNumber uint64
	if zkBlock != nil {
		blockHash = zkBlock.BlockHash.Hex()
		blockNumber = zkBlock.BlockNumber
	}

	if cfg.Features.AvcValidation.Mode == "enforce" {
		if avcErr != nil {
			// Fail closed: in enforce mode the new path IS the decision, so an
			// internal error must reject, never silently fall back to legacy.
			if l := logger(); l != nil {
				l.Error(ctx, "AVC_ENFORCE_ERROR: avc validation errored — failing closed (rejecting block)", avcErr,
					ion.String("block_hash", blockHash), ion.Int("block_number", int(blockNumber)))
			}
			return false, fmt.Errorf("adapters.EvaluateShadow: enforce-mode avc validation errored, failing closed: %w", avcErr)
		}
		if avcAccept != legacyAccept {
			if l := logger(); l != nil {
				l.Info(ctx, "AVC_ENFORCE_OVERRIDE: avc decision differs from legacy — using avc decision (enforce mode)",
					ion.String("block_hash", blockHash), ion.Int("block_number", int(blockNumber)))
			}
		}
		return avcAccept, nil
	}

	// Shadow mode (default/safe): compare and log only; legacy decision stands.
	if avcErr != nil || avcAccept != legacyAccept {
		if l := logger(); l != nil {
			l.Warn(ctx, "AVC_SHADOW_MISMATCH: new avc validator disagrees with (or errored against) the legacy decision — legacy decision still governs the vote",
				ion.String("block_hash", blockHash),
				ion.Int("block_number", int(blockNumber)),
				ion.Bool("legacy_accept", legacyAccept),
				ion.Bool("avc_accept", avcAccept))
		}
	}
	return legacyAccept, legacyErr
}

// runFullValidatorAgainstDB builds a real avc FullValidator wired to jmdn's
// real Security package and a freshly-populated SecurityCache (mirroring
// Security.CheckZKBlockValidation's own cache lifecycle: new per call, loaded
// once via LoadAccounts, closed on return), then runs DepthFull validation.
//
// UNTESTED: this function's account-loading path (DB_OPs.GetAccountConnectionandPutBack
// + SecurityCache.LoadAccounts against real ImmuDB) has NOT been exercised by
// any test in this repo — every existing adapter test populates the cache via
// Security.RegisterAccount (in-memory, no DB). Validate this against a live
// ImmuDB instance before relying on shadow-mode output, and before ever
// enabling "enforce" mode. See task: "Confirm real chain IDs and DB
// population path before wiring."
//
// PERFORMANCE NOTE: when Features.AvcValidation.Enabled is on, this runs a
// SECOND full account load per block (in addition to the one
// CheckZKBlockValidation already does) — i.e. it roughly doubles ImmuDB read
// load for every validated block on that node. Acceptable for a small,
// testnet-only shadow rollout; would need addressing (e.g. sharing one cache)
// before any broader/mainnet rollout.
func runFullValidatorAgainstDB(ctx context.Context, cfg *settings.NodeConfig, zkBlock *config.ZKBlock) (bool, error) {
	if zkBlock == nil {
		return false, fmt.Errorf("adapters.runFullValidatorAgainstDB: nil zkBlock")
	}
	if len(zkBlock.Transactions) == 0 {
		return false, fmt.Errorf("adapters.runFullValidatorAgainstDB: zkBlock has no transactions")
	}

	accountsConn, err := DB_OPs.GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return false, fmt.Errorf("adapters.runFullValidatorAgainstDB: get accounts connection: %w", err)
	}
	defer DB_OPs.PutAccountsConnection(accountsConn)

	cache := Security.NewSecurityCache()
	defer cache.Close()

	accountsSet := DB_OPs.NewAccountsSet()
	for _, tx := range zkBlock.Transactions {
		if tx.From != nil {
			accountsSet.Add(*tx.From)
		}
		if tx.To != nil {
			accountsSet.Add(*tx.To)
		}
	}
	if err := cache.LoadAccounts(ctx, accountsConn, accountsSet); err != nil {
		return false, fmt.Errorf("adapters.runFullValidatorAgainstDB: load accounts: %w", err)
	}

	chainID := big.NewInt(int64(cfg.Network.ChainID))
	stateless, err := NewStatelessChecker(chainID)
	if err != nil {
		return false, fmt.Errorf("adapters.runFullValidatorAgainstDB: build stateless checker: %w", err)
	}
	stateful, err := NewStatefulChecker(cache)
	if err != nil {
		return false, fmt.Errorf("adapters.runFullValidatorAgainstDB: build stateful checker: %w", err)
	}

	fv := validation.NewFullValidator(stateless, stateful, 0) // 0 = runtime.GOMAXPROCS(0)
	ad := NewZKBlockAdapter(zkBlock)
	verdict, err := fv.ValidateBlock(ad, interfaces.DepthFull)
	if err != nil {
		return false, fmt.Errorf("adapters.runFullValidatorAgainstDB: validate block: %w", err)
	}
	if !verdict.Accept {
		// Surface WHY the avc validator rejected. In enforce mode this verdict
		// BECOMES the vote decision, but its reason is otherwise dropped — the
		// vote then logs only the generic "validation returned false". %+v avoids
		// hard-coding avc Verdict field names (defined in ../avc).
		if l := logger(); l != nil {
			l.Warn(ctx, "AVC_VALIDATOR_REJECT: full validator rejected block (reason below)",
				ion.String("verdict", fmt.Sprintf("%+v", verdict)),
				ion.Int("block_number", int(zkBlock.BlockNumber)),
				ion.String("block_hash", zkBlock.BlockHash.Hex()))
		}
	}
	return verdict.Accept, nil
}
