package messaging

import (
	"context"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"

	"gossipnode/Security"
	"gossipnode/config"
)

// ValidateSyncedBlockContents runs, for a block received by catch-up sync, the
// same content checks the live receive path applies before the certificate:
// every transaction's signature recovers its declared sender for this chain,
// its tx hash equals the hash of its contents, values are non-negative, nonces
// ascend per sender, and BlockHash/TxnsRoot cover exactly these transactions.
// A block that carries a ConsensusHash must also match it.
//
// Without this, a synced block was bound to its transactions only through the
// carried tx.Hash values, so the contents behind a valid certificate (sender,
// recipient, value) could differ from what the committee signed.
//
// Typed transactions read back from storage before the chain id was persisted
// carry no ChainID; it is filled with this node's configured chain id (the
// signature check requires exactly that id anyway) so their contents re-hash.
func ValidateSyncedBlockContents(ctx context.Context, b *config.ZKBlock) error {
	if b == nil {
		return errors.New("nil block")
	}
	fillTypedTxChainID(b)
	if rej := validateBlockContents(ctx, b); rej != nil {
		return rej.err
	}
	if !EnforceBodyBinding {
		// validateBlockContents binds BlockHash to the transactions only under
		// EnforceBodyBinding; the sync path requires it unconditionally.
		if ok, err := Security.CheckBlockHash(b); err != nil || !ok {
			if err == nil {
				err = errors.New("block hash does not match transaction contents")
			}
			return err
		}
	}
	// Unconditional, same as the live receive path: checkConsensusBinding
	// itself rejects a missing ConsensusHash (D-28 — a missing hash was the
	// dedup/equivocation collision bypass this check exists to close). Gating
	// the call on a non-zero hash would let a synced block with no
	// ConsensusHash skip this check entirely.
	if rej := checkConsensusBinding(b); rej != nil {
		return rej.err
	}
	return nil
}

// FinalizeSyncedBlockChecks runs the checks that follow certificate
// verification on the live path: FeeRecipients recomputation (when the reward
// split is enabled) and the durable one-block-per-height equivocation record.
func FinalizeSyncedBlockChecks(b *config.ZKBlock) error {
	if b == nil {
		return errors.New("nil block")
	}
	if rewardSplitEnabled() {
		if rej := checkFeeRecipients(b); rej != nil {
			return rej.err
		}
	}
	if rej := checkEquivocation(b.BlockNumber, b.ConsensusHashHex()); rej != nil {
		return rej.err
	}
	return nil
}

func fillTypedTxChainID(b *config.ZKBlock) {
	id := Security.ExpectedChainID()
	if id == nil {
		return
	}
	for i := range b.Transactions {
		tx := &b.Transactions[i]
		if tx.ChainID == nil && (tx.Type == types.AccessListTxType || tx.Type == types.DynamicFeeTxType) {
			tx.ChainID = new(big.Int).Set(id)
		}
	}
}
