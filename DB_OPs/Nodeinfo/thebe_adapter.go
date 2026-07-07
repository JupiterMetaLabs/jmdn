package NodeInfo

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"gossipnode/DB_OPs"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/checksum/checksum_priorsync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
)

const ChecksumVersion = 2

type sync_struct struct{}

// lastBlockReceivedNs holds the Unix nanosecond timestamp of the last successful
// block write (WriteData or WriteHeaders). Updated atomically from write paths;
// read by the SyncMonitor propagation guard via LastBlockReceivedAt().
var lastBlockReceivedNs atomic.Int64

// notifyBlockReceived records the current wall-clock time as the last block-received
// timestamp. Called from WriteData and WriteHeaders after a successful DB write.
func notifyBlockReceived() {
	lastBlockReceivedNs.Store(time.Now().UnixNano())
}

// LastBlockReceivedAt satisfies the syncmonitor.blockTimer interface.
// Returns zero time if no block has ever been written (propagation guard skips).
func (sync *sync_struct) LastBlockReceivedAt() time.Time {
	ns := lastBlockReceivedNs.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Time Complexity: O(1)
// NewSyncStruct initializes the synchronization struct that satisfies types.BlockInfo.
func NewSyncStruct() types.BlockInfo {
	return &sync_struct{}
}

// Time Complexity: O(1) mostly, bounded by network round trip to ThebeDB.
// GetBlockNumber retrieves the latest block number.
func (sync *sync_struct) GetBlockNumber() uint64 {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	num, err := DB_OPs.GetLatestBlockNumber(ctx, nil)
	if err != nil {
		log.Printf("[NodeInfo] ERROR: GetLatestBlockNumber failed: %v. Attempting manual reconciliation.", err)
		return 0
	}
	return num
}

// Time Complexity: O(1) bounded by single block DB lookup
// GetBlockDetails fetches the latest block headers and returns a checksum wrapped in a PriorSync struct.
func (sync *sync_struct) GetBlockDetails() types.PriorSync {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	latestNum, err := DB_OPs.GetLatestBlockNumber(ctx, nil)
	if err != nil {
		log.Printf("Error getting latest block number for GetBlockDetails: %v", err)
		return types.PriorSync{}
	}

	latestBlock, err := DB_OPs.GetZKBlockByNumber(nil, latestNum)
	if err != nil {
		log.Printf("Error getting latest block details: %v", err)
		return types.PriorSync{}
	}

	priorsync := &types.PriorSync{
		Metadata: types.Metadata{},
	}
	if latestBlock != nil {
		priorsync.Blocknumber = latestBlock.BlockNumber
		priorsync.Blockhash = latestBlock.BlockHash[:]
		priorsync.Stateroot = latestBlock.StateRoot[:]
	}

	checksumBytes, err := checksum_priorsync.PriorSyncChecksum().Create(*priorsync, ChecksumVersion)
	if err != nil {
		log.Printf("Error creating checksum: %v", err)
		return types.PriorSync{}
	}
	priorsync.Metadata.Checksum = checksumBytes
	priorsync.Metadata.Version = ChecksumVersion

	return *priorsync
}

// ReconcileBlockNumber performs an authoritative scan of immudb to find the true
// highest contiguous block present, bypassing the potentially-stale "latest_block"
// marker. Called by FastsyncV2.reconcileLocalLatestBlock() before each catchup.
//
// Use case: blocks written by propagation (PubSub) may advance the immudb key-space
// beyond the stored "latest_block" marker — e.g. after a crash mid-write or when
// the DataSync writer advances blocks before updating the marker. Without this, the
// catchup scan range is anchored to a stale head, causing the node to report a lower
// Merkle fingerprint to the seednode than necessary.
//
// Bound: scans at most reconcileScanAhead blocks ahead of the marker in a single
// batch read (one immudb round-trip). In practice the marker is never more than a
// few hundred blocks stale; this bound is conservative and keeps the call O(1).
func (sync *sync_struct) ReconcileBlockNumber() uint64 {
	const reconcileScanAhead = uint64(500)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	base := sync.GetBlockNumber()
	if base == 0 {
		return 0
	}

	conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
	if err != nil {
		log.Printf("[NodeInfo] ReconcileBlockNumber: DB connection failed: %v — returning marker %d", err, base)
		return base
	}

	// Single batch read: [base+1 .. base+reconcileScanAhead]
	scanEnd := base + reconcileScanAhead
	candidates, err := DB_OPs.GetBlocksRange(conn, base+1, scanEnd)
	if err != nil {
		log.Printf("[NodeInfo] ReconcileBlockNumber: GetBlocksRange failed: %v — returning marker %d", err, base)
		return base
	}
	if len(candidates) == 0 {
		return base
	}

	// Build a presence set, then walk forward to find the highest contiguous block.
	present := make(map[uint64]bool, len(candidates))
	for _, b := range candidates {
		if b != nil {
			present[b.BlockNumber] = true
		}
	}

	highest := base
	for n := base + 1; n <= scanEnd; n++ {
		if !present[n] {
			break // contiguous run ended
		}
		highest = n
	}

	if highest > base {
		log.Printf("[NodeInfo] ReconcileBlockNumber: marker=%d true_head=%d (%d untracked block(s))",
			base, highest, highest-base)
	}
	return highest
}

// Time Complexity: O(1)
// NewAccountManager returns the ThebeDB implementation of AccountManager.
func (sync *sync_struct) NewAccountManager() types.AccountManager {
	return &account_manager{}
}
