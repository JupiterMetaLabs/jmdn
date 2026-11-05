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
	"gossipnode/messaging"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Applier implements thebesync.BlockApplier over DB_OPs + the local apply path.
// Source is the peer the blocks are fetched from; it is checked only for
// legacy blocks (legacy follow mode), which must come from the pinned main
// sequencer.
type Applier struct {
	Source peer.ID
}

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
func (a Applier) Apply(raw []byte, prevNumber uint64, prevHash string, requireCert bool) (uint64, string, bool, error) {
	var blk config.ZKBlock
	if err := json.Unmarshal(raw, &blk); err != nil {
		return 0, "", false, fmt.Errorf("thebesync applier: unmarshal block: %w", err)
	}
	// Legacy follow mode: a legacy block is accepted only from the pinned main
	// sequencer and through the legacy gate (see legacy_follow.go).
	if messaging.IsLegacyBlock(&blk) {
		if !messaging.AuthorizeLegacySource(a.Source.String()) {
			return 0, "", false, fmt.Errorf("thebesync applier: legacy block %d fetched from %q, not the pinned main sequencer", blk.BlockNumber, a.Source.String())
		}
		if err := applyLegacyBlock(context.Background(), &blk, prevNumber, common.HexToHash(prevHash)); err != nil {
			return 0, "", false, err
		}
		return blk.BlockNumber, blk.BlockHash.Hex(), false, nil
	}
	hasCert, err := applyBlock(context.Background(), &blk, prevNumber, common.HexToHash(prevHash), requireCert)
	if err != nil {
		return 0, "", false, err
	}
	return blk.BlockNumber, blk.BlockHash.Hex(), hasCert, nil
}
