// Package thebesyncserve serves the ThebeSync (FastSync v4) read-only protocols
// from a main-branch (ImmuDB) node, so v3base nodes following the mainnet
// sequencer during the migration can catch up on blocks they missed.
//
// Serve-only: it answers /fastsync/v4/head and /fastsync/v4/getblocks requests
// and never writes. The wire format, framing and serve limits are copied from
// JMDN-FastSync thebesync v0.1.0-v3base.6 (server side only) so main does not
// take the v3base FastSync module. Remove after the migration to v3base.
package thebesyncserve

import "github.com/libp2p/go-libp2p/core/protocol"

const (
	// Protocol IDs, identical to JMDN-FastSync thebesync.
	HeadProtocol      protocol.ID = "/fastsync/v4/head"
	GetBlocksProtocol protocol.ID = "/fastsync/v4/getblocks"

	// MaxBlocksPerRequest bounds a single GetBlocks range.
	MaxBlocksPerRequest = 30

	maxHeadReqBytes      int64 = 512
	maxGetBlocksReqBytes int64 = 512
)

// maxGetBlocksRespBytes is the client's response cap; the serve byte budget is
// derived from it (see serveByteBudget).
var maxGetBlocksRespBytes int64 = 128 * 1024 * 1024

// HeadResponse reports the node's chain tip. Error is set when it cannot serve.
type HeadResponse struct {
	Height       uint64 `json:"height"`
	TipBlockHash string `json:"tip_block_hash"`
	Error        string `json:"error,omitempty"`
}

// GetBlocksRequest asks for the inclusive block range [From..To].
type GetBlocksRequest struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// GetBlocksResponse returns opaque serialized blocks, ascending by height.
type GetBlocksResponse struct {
	Blocks [][]byte `json:"blocks"`
	Error  string   `json:"error,omitempty"`
}

// BlockProvider is the server-side block source.
type BlockProvider interface {
	// LatestHeight returns the local chain tip; found=false on an empty chain.
	LatestHeight() (height uint64, found bool, err error)
	// TipHash returns the hex block hash at height.
	TipHash(height uint64) (string, error)
	// RawBlock returns the serialized block at n; found=false beyond the tip.
	RawBlock(n uint64) (raw []byte, found bool, err error)
}
