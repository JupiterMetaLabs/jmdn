package thebesync

// ChainReporter supplies the sync monitor's periodic report inputs from the local
// ThebeDB chain. It replaces the old FastsyncV2 MMR fingerprint (O(N) Merkle tree
// over block hashes, via the retired JMDN-FastSync BlockInfo) with an O(1) read of
// the tip block's cumulative StateRoot.
//
// The reported root is the tip block's StateRoot:
//
//	StateRoot_n = Keccak256(StateRoot_{n-1} || BlockHash_n)
//
// a 32-byte cumulative commitment over the entire block-hash history, already
// stored on every block and identical across honest nodes at the same height —
// exactly the cross-node comparison the seednode needs, with no tree rebuild.

import (
	"context"
	"time"

	"gossipnode/DB_OPs"
)

// ChainReporter implements the sync monitor's reporter interface over DB_OPs.
type ChainReporter struct{}

// TipState returns the local tip height and its StateRoot (32 bytes). An empty
// chain yields (0, nil, error) — the monitor treats that as "nothing to report".
func (ChainReporter) TipState(ctx context.Context) (uint64, []byte, error) {
	head, err := DB_OPs.GetLatestBlockNumber(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	blk, err := DB_OPs.GetZKBlockByNumber(nil, head)
	if err != nil {
		return 0, nil, err
	}
	// StateRoot is the cumulative block-hash commitment; read directly, O(1).
	return head, blk.StateRoot.Bytes(), nil
}

// LastBlockReceivedAt returns when the most recent block was durably stored,
// for the monitor's propagation guard (skip a report that races an in-flight
// block write). Zero time when no block has been stored this process.
func (ChainReporter) LastBlockReceivedAt() time.Time { return DB_OPs.LastBlockStoredAt() }
