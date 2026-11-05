package thebesyncserve

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
)

// Provider serves blocks from this node's ImmuDB block store as JSON-encoded
// config.ZKBlock, the same bytes thebe-export reads (block:<n>).
type Provider struct {
	// Seams for tests; nil means the DB_OPs functions.
	latest func(ctx context.Context) (uint64, error)
	block  func(n uint64) (*config.ZKBlock, error)
}

func (p Provider) latestHeight(ctx context.Context) (uint64, error) {
	if p.latest != nil {
		return p.latest(ctx)
	}
	return DB_OPs.GetLatestBlockNumber(ctx, nil)
}

func (p Provider) getBlock(n uint64) (*config.ZKBlock, error) {
	if p.block != nil {
		return p.block(n)
	}
	return DB_OPs.GetZKBlockByNumber(nil, n)
}

// LatestHeight returns the local tip. A chain with no blocks reports found=false.
func (p Provider) LatestHeight() (uint64, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := p.latestHeight(ctx)
	if err != nil {
		return 0, false, err
	}
	return h, true, nil
}

// TipHash returns the hex block hash at height.
func (p Provider) TipHash(height uint64) (string, error) {
	blk, err := p.getBlock(height)
	if err != nil {
		return "", err
	}
	return blk.BlockHash.Hex(), nil
}

// RawBlock returns the JSON-encoded block at n; found=false when n is beyond
// the local tip.
func (p Provider) RawBlock(n uint64) ([]byte, bool, error) {
	tip, found, err := p.LatestHeight()
	if err != nil {
		return nil, false, err
	}
	if !found || n > tip {
		return nil, false, nil
	}
	blk, err := p.getBlock(n)
	if err != nil {
		return nil, false, fmt.Errorf("thebesyncserve: block %d: %w", n, err)
	}
	raw, err := json.Marshal(blk)
	if err != nil {
		return nil, false, fmt.Errorf("thebesyncserve: encode block %d: %w", n, err)
	}
	return raw, true, nil
}
