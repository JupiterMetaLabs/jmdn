package adapters

import (
	"context"
	"fmt"
	"math/big"

	"github.com/JupiterMetaLabs/avc/interfaces"

	"gossipnode/Security"
)

// StatelessChecker implements avc's interfaces.StatelessTxChecker by wrapping
// jmdn's EXISTING stateless validation — it does not reimplement any crypto.
//
// Phase-1 checks, in the order jmdn's allChecksWithConn runs them (minus the
// stateful ones, which live in StatefulChecker):
//
//  1. chain id present and equal to the configured expected chain id
//  2. transaction values non-negative (jmdn's Security.CheckTransactionValues —
//     a stateless negative-value gate jmdn applies at every trust boundary)
//  3. transaction hash matches the content hash (Security.CheckTransactionHash)
//  4. signature recovers to tx.From (Security.CheckSignature)
//
// All four need only the transaction bytes + a configured chain id — no DB,
// no account state — which is why they are safe to run in parallel across a
// block (avc's ParallelStatelessValidator).
//
// jmdn's Security.CheckSignature relies on a package-level signer cache built
// from the expected chain id (SetExpectedChainID / rebuildSignerCache). The
// constructor sets that here so signature recovery and the chain-id comparison
// use the SAME chain id and cannot drift.
type StatelessChecker struct {
	expectedChainID *big.Int
}

// NewStatelessChecker builds the checker for a given chain id and configures
// jmdn's Security signer cache to match. chainID must be a positive value —
// a zero/nil chain id is refused (fail-closed): jmdn's signature verification
// would otherwise be unconfigured and reject every transaction.
func NewStatelessChecker(chainID *big.Int) (*StatelessChecker, error) {
	if chainID == nil || chainID.Sign() <= 0 {
		return nil, fmt.Errorf("adapters.NewStatelessChecker: chain id must be > 0, got %v", chainID)
	}
	id := new(big.Int).Set(chainID)
	// Build jmdn's cached signers from this chain id so CheckSignature is
	// consistent with the chain-id comparison below.
	Security.SetExpectedChainIDBig(id)
	return &StatelessChecker{expectedChainID: id}, nil
}

// CheckTx implements interfaces.StatelessTxChecker.
func (c *StatelessChecker) CheckTx(ctx context.Context, tx interfaces.Transaction) error {
	jb, ok := tx.(jmdnBacked)
	if !ok {
		return fmt.Errorf("adapters.StatelessChecker: transaction is not jmdn-backed (%T) — refusing (fail-closed)", tx)
	}
	t := jb.JMDNTransaction()

	// 1. chain id
	if t.ChainID == nil {
		return fmt.Errorf("adapters.StatelessChecker: tx %s has no chain id", t.Hash.Hex())
	}
	if t.ChainID.Cmp(c.expectedChainID) != 0 {
		return fmt.Errorf("adapters.StatelessChecker: chain id mismatch for tx %s: got %s, expected %s",
			t.Hash.Hex(), t.ChainID, c.expectedChainID)
	}

	// 2. value gate (stateless): reject negative value / fee fields
	if ok, err := Security.CheckTransactionValues(&t); !ok || err != nil {
		return fmt.Errorf("adapters.StatelessChecker: value check failed for tx %s: %v", t.Hash.Hex(), err)
	}

	// 3. transaction hash matches content
	if ok, err := Security.CheckTransactionHash(&t, ctx); !ok || err != nil {
		return fmt.Errorf("adapters.StatelessChecker: tx hash check failed for tx %s: %v", t.Hash.Hex(), err)
	}

	// 4. signature recovers to From
	if ok, err := Security.CheckSignature(&t, ctx); !ok || err != nil {
		return fmt.Errorf("adapters.StatelessChecker: signature check failed for tx %s: %v", t.Hash.Hex(), err)
	}

	return nil
}

// Compile-time assertion.
var _ interfaces.StatelessTxChecker = (*StatelessChecker)(nil)
