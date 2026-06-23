package NodeInfo

import (
	"context"
	"log"
	"time"

	"gossipnode/DB_OPs"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/checksum/checksum_priorsync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
)

const ChecksumVersion = 2

type sync_struct struct{}

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

// Time Complexity: O(1)
// NewAccountManager returns the ThebeDB implementation of AccountManager.
func (sync *sync_struct) NewAccountManager() types.AccountManager {
	return &account_manager{}
}
