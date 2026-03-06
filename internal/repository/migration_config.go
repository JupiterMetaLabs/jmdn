package repository

import "time"

// Config holds the configuration for the background backfill worker.
type Config struct {
	// Enabled determines if the backfill worker should run at startup.
	Enabled bool

	// MaxBlocksPerBatch is the maximum number of blocks to process before sleeping.
	MaxBlocksPerBatch int

	// MaxAccountsPerBatch is the maximum number of accounts to process before sleeping.
	MaxAccountsPerBatch int

	// ThrottleDuration is the sleep time between batches to prevent overwhelming the node.
	ThrottleDuration time.Duration

	// MigrateBlocks determines if historical blocks should be migrated.
	MigrateBlocks bool

	// MigrateAccounts determines if historical accounts should be migrated.
	MigrateAccounts bool
}

// DefaultConfig provides safe default settings for production use.
func DefaultConfig() Config {
	return Config{
		Enabled:             true,
		MaxBlocksPerBatch:   50,
		MaxAccountsPerBatch: 100,
		ThrottleDuration:    200 * time.Millisecond,
		MigrateBlocks:       true,
		MigrateAccounts:     true,
	}
}
