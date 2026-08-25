// MODULE: DB_OPs/genesis_block
// PURPOSE: Write a block 0 on a FRESH chain so "latest"-anchored reads resolve.
//
// JMDN produces blocks only from the sequencer/orchestrator, and has no genesis
// block. On a brand-new chain that means GetLatestBlockNumber returns "no rows",
// so eth_getBalance, the explorer's checkForNewBlocks, and the orchestrator's
// balance validation all fail — and the first produced block has no parent to link
// to. Seeding an empty block 0 (carrying the already-seeded genesis account state)
// fixes the whole class: latest=0, balance/nonce reads resolve, and block 1 links
// to a real parent hash.
//
// Idempotent: a no-op once any block exists. Devnet/bootstrap only — call it
// alongside genesis account seeding (JMDN_GENESIS_ALLOC), never on a chain that
// already has history.
package DB_OPs

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/config"
)

// GenesisBlockHash is the deterministic hash stamped on block 0 (empty, no txs).
// It is the parent every node/orchestrator will link block 1 against.
var GenesisBlockHash = crypto.Keccak256Hash([]byte("jmdn/genesis-block/v1"))

// SeedGenesisBlockIfEmpty writes block 0 when the chain has no blocks. Returns
// (true, nil) if it wrote one, (false, nil) if a block already existed.
func SeedGenesisBlockIfEmpty(ctx context.Context) (bool, error) {
	if _, err := GetLatestBlockNumber(ctx, nil); err == nil {
		return false, nil // chain already has at least one block
	} else if !isNotFoundError(err) {
		return false, fmt.Errorf("genesis block: probing latest block: %w", err)
	}

	zero := common.Address{}
	blk := &config.ZKBlock{
		BlockNumber:  0,
		Timestamp:    1_700_000_000,
		Transactions: nil,
		PrevHash:     common.Hash{},
		BlockHash:    GenesisBlockHash,
		CoinbaseAddr: &zero,
		ZKVMAddr:     &zero,
		GasLimit:     30_000_000,
		Status:       "genesis",
	}
	if err := StoreZKBlock(nil, blk); err != nil {
		return false, fmt.Errorf("genesis block: store: %w", err)
	}
	return true, nil
}
