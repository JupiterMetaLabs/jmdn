package repository

import (
	"os"
	"time"
)

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
// Backfill is disabled by default — enable explicitly via BACKFILL_ENABLED=true.
func DefaultConfig() Config {
	return Config{
		Enabled:             false,
		MaxBlocksPerBatch:   50,
		MaxAccountsPerBatch: 100,
		ThrottleDuration:    200 * time.Millisecond,
		MigrateBlocks:       true,
		MigrateAccounts:     true,
	}
}

// ConfigFromEnv builds a Config from DefaultConfig, overriding Enabled from
// the BACKFILL_ENABLED environment variable when present.
func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	if v := os.Getenv("BACKFILL_ENABLED"); v == "true" || v == "1" {
		cfg.Enabled = true
	}
	return cfg
}
