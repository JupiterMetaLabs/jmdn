package thebesync

// Provider adapts jmdn's block store to the JMDN-FastSync thebesync.BlockProvider
// interface (structural — no import of the library needed here). It serves blocks
// as opaque JSON-marshalled config.ZKBlock bytes; the library ships them verbatim
// and the receiver's Applier parses them back.

import (
	"context"
	"encoding/json"
	"fmt"

	"gossipnode/DB_OPs"
)

// Provider implements thebesync.BlockProvider over DB_OPs.
type Provider struct{}

// LatestHeight returns the local tip height; found=false on an empty chain.
func (Provider) LatestHeight() (uint64, bool, error) {
	h, err := DB_OPs.GetLatestBlockNumber(context.Background(), nil)
	if err != nil {
		if DB_OPs.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return h, true, nil
}

// TipHash returns the hex block hash at height.
func (Provider) TipHash(height uint64) (string, error) {
	blk, err := DB_OPs.GetZKBlockByNumber(nil, height)
	if err != nil {
		return "", err
	}
	return blk.BlockHash.Hex(), nil
}

// RawBlock returns the opaque serialized block at n; found=false when n is beyond
// the local tip (end of chain), which the library treats as "tip reached".
func (Provider) RawBlock(n uint64) ([]byte, bool, error) {
	blk, err := DB_OPs.GetZKBlockByNumber(nil, n)
	if err != nil {
		if DB_OPs.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	// Fail-closed: a block with transactions MUST carry the ART identities
	// (EnrichBlockAccountNonces stamps every touched account). Serving one without
	// them ships a block the receiver cannot apply — it fails on the first
	// new-account with "no block-carried ART identity". Refuse loudly here instead
	// so the operator backfills (JMDN_BACKFILL_ACCOUNT_NONCES=1) rather than
	// chasing a mid-sync apply failure. Genesis / empty blocks legitimately carry
	// none, so the guard is scoped to blocks that actually touch accounts.
	if len(blk.Transactions) > 0 && len(blk.AccountNonces) == 0 {
		return nil, false, fmt.Errorf("thebesync provider: block %d has %d transaction(s) but no AccountNonces — refusing to serve an unstampable block (run JMDN_BACKFILL_ACCOUNT_NONCES=1 on this node)", n, len(blk.Transactions))
	}
	raw, err := json.Marshal(blk)
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}
