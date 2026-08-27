package thebesync

// Applier adapts jmdn's verify+apply path to the JMDN-FastSync
// thebesync.BlockApplier interface (structural — no import of the library). It
// parses the opaque bytes back into a config.ZKBlock and runs the hardened
// verification+apply in apply.go, then reports the applied block's identity so the
// library's receiver can chain to the next block.

import (
	"context"
	"encoding/json"
	"fmt"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// Applier implements thebesync.BlockApplier over DB_OPs + the local apply path.
type Applier struct{}

// LocalTip returns the local applied tip (height + hex hash). On a fresh node this
// is the locally-seeded genesis block.
func (Applier) LocalTip() (uint64, string, error) {
	h, err := DB_OPs.GetLatestBlockNumber(context.Background(), nil)
	if err != nil {
		return 0, "", err
	}
	blk, err := DB_OPs.GetZKBlockByNumber(nil, h)
	if err != nil {
		return 0, "", err
	}
	return h, blk.BlockHash.Hex(), nil
}

// Apply parses one opaque block and runs the full verify+apply path against the
// (prevNumber, prevHash) anchor, returning the applied block's (number, hash) and
// whether it carried a verified committee certificate.
func (Applier) Apply(raw []byte, prevNumber uint64, prevHash string, requireCert bool) (uint64, string, bool, error) {
	var blk config.ZKBlock
	if err := json.Unmarshal(raw, &blk); err != nil {
		return 0, "", false, fmt.Errorf("thebesync applier: unmarshal block: %w", err)
	}
	hasCert, err := applyBlock(context.Background(), &blk, prevNumber, common.HexToHash(prevHash), requireCert)
	if err != nil {
		return 0, "", false, err
	}
	return blk.BlockNumber, blk.BlockHash.Hex(), hasCert, nil
}
