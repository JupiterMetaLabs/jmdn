package Block

import (
	"fmt"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/config"
)

// checkProposalHeader refuses a submitted block that every voter would refuse
// under header binding (consensus.header_binding_from_height): a BlockHash
// that does not cover the coinbase, zkvm address and timestamp, addresses
// other than the configured ones, or a timestamp before the parent's or too
// far in the future. Refusing here gives the orchestrator the reason at once
// instead of a failed round. A no-op below the activation height.
func checkProposalHeader(block *config.ZKBlock) error {
	if block == nil || !Security.HeaderBound(block.BlockNumber) {
		return nil
	}
	if ok, err := Security.CheckBlockHash(block); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("block hash mismatch")
		}
		return fmt.Errorf("header binding: %w", err)
	}
	var parentTimestamp int64
	if block.BlockNumber > 0 {
		parent, err := DB_OPs.GetZKBlockHeaderByNumber(block.BlockNumber - 1)
		if err != nil {
			return fmt.Errorf("header binding: read parent block %d: %w", block.BlockNumber-1, err)
		}
		parentTimestamp = parent.Timestamp
	}
	return Security.CheckHeaderFields(block, parentTimestamp, time.Now())
}
