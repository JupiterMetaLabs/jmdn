// Package settings provides structured configuration for JMDN nodes.
// Configuration is loaded from YAML files, environment variables, and CLI flags
// using Viper, with the following priority: Flags > Env > Config File > Defaults.
package settings

import (
	"time"
)

// NodeConfig is the top-level configuration for a JMDN node.
// Each section maps to a YAML key in jmdn.yaml.
type NodeConfig struct {
	Node         NodeSettings       `mapstructure:"node"`
	Network      NetworkSettings    `mapstructure:"network"`
	Ports        PortSettings       `mapstructure:"ports"`
	Binds        BindSettings       `mapstructure:"binds"`
	Database     DatabaseSettings   `mapstructure:"database"`
	Logging      LoggingSettings    `mapstructure:"logging"`
	Features     FeatureSettings    `mapstructure:"features"`
	Security     SecurityConfig     `mapstructure:"security"`
	Alerts       AlertsConfig       `mapstructure:"alerts"`
	Orchestrator OrchestratorConfig `mapstructure:"orchestrator"`
	FastSync     FastSyncSettings   `mapstructure:"fastsync"`
	Selection    SelectionSettings  `mapstructure:"selection"`
	Consensus    ConsensusSettings  `mapstructure:"consensus"`
}

// ConsensusSettings holds operator-controlled consensus policy.
//
// BlockBuddy is an operator blocklist of committee peer IDs. Any peer_id listed
// here is EXCLUDED from the eligible committee even if the seedNode buddy
// selection (getBuddy/ListBuddy) returns it — a manual kill-switch for a peer
// the operator no longer trusts, without waiting for seedNode to drop it.
//
// YAML:
//
//	consensus:
//	  block_buddy:
//	    - "12D3KooW...badpeer1"
//	    - "12D3KooW...badpeer2"
//
// Env (highest priority): JMDN_CONSENSUS_BLOCK_BUDDY (space-separated list).
type ConsensusSettings struct {
	BlockBuddy []string `mapstructure:"block_buddy" yaml:"block_buddy"`

	// Committee-source integration with the seedNodes authenticated
	// committee. All three must align with the seed deployment.
	//
	// SeedAuthorityBLSPub is the PINNED dela/bls authority public key (lowercase
	// hex) distributed out-of-band (genesis/config). A committee snapshot signed
	// by any other key is rejected. Empty => snapshot verification cannot pin and
	// the consumer stays disabled (fail-closed; no committee source).
	SeedAuthorityBLSPub string `mapstructure:"seed_authority_bls_pub" yaml:"seed_authority_bls_pub"`
	// CommitteeEpochSeconds is the shared epoch clock divisor (unix/seconds).
	// MUST equal the seed's COMMITTEE_EPOCH_SECONDS (default 3600).
	CommitteeEpochSeconds int64 `mapstructure:"committee_epoch_seconds" yaml:"committee_epoch_seconds"`

	// MaxValidators HARD-CAPS the number of buddy (validator) nodes counted toward
	// consensus. The certificate verifier trims the eligible committee to this many
	// peers (deterministically, by sorted peer_id) BEFORE computing the 2f+1
	// threshold, so the threshold can never be sized over more validators than
	// actually vote. Defaults to 5 (must match config.MaxMainPeers, the voting
	// committee size) and is always active — never 0 by default. An explicit 0
	// disables the cap, but that is not the shipped behavior.
	MaxValidators int `mapstructure:"max_validators" yaml:"max_validators"`

	// P2P controls direct per-peer (one-to-one) block propagation over libp2p
	// streams, IN ADDITION to the gossip mesh. Default 1 = direct + gossip both
	// active (dedup + the per-block-hash apply lock make the double delivery
	// safe); this is the resilient default because it does not depend solely on
	// gossip-mesh reachability. Set to 0 for gossip-only (the mesh + FloodPublish
	// reach the whole fleet with no redundant direct fan-out).
	//
	// Applies per node: at 1 the sequencer originates the direct fan-out AND every
	// node hop-forwards, so direct only works fleet-wide when set on all nodes.
	// Also overridable via JMDN_DIRECT_BLOCK_PROPAGATION=1 (env forces direct on).
	//
	// YAML (opt into gossip-only):
	//
	//	consensus:
	//	  p2p: 0
	P2P int `mapstructure:"p2p" yaml:"p2p"`
}

// SelectionSettings holds the SECRET VRF key material used for node / committee
// selection. These MUST be unique per network and kept secret. Empty values are
// rejected at use time (fail-closed) rather than falling back to a default.
//
// YAML:
//
//	selection:
//	  mnemonic: "<network secret BIP39 mnemonic>"
//	  salt: "<network VRF salt>"
//
// Env (highest priority): JMDN_NODE_SELECTION_MNEMONIC, JMDN_NETWORK_SALT.
type SelectionSettings struct {
	Mnemonic string `mapstructure:"mnemonic" yaml:"mnemonic"`
	Salt     string `mapstructure:"salt"     yaml:"salt"`
}

// NodeSettings defines the identity of this node.
type NodeSettings struct {
	Alias string `mapstructure:"alias" yaml:"alias"`
}

// NetworkSettings controls peer-to-peer connectivity.
type NetworkSettings struct {
	ChainID           int    `mapstructure:"chain_id"           yaml:"chain_id"`
	SeedNode          string `mapstructure:"seednode"           yaml:"seednode"`
	Mempool           string `mapstructure:"mempool"            yaml:"mempool"`
	Yggdrasil         bool   `mapstructure:"yggdrasil"          yaml:"yggdrasil"`
	HeartbeatInterval int    `mapstructure:"heartbeat_interval" yaml:"heartbeat_interval"`
}

// PortSettings groups all port/address assignments.
type PortSettings struct {
	API       int `mapstructure:"api"       yaml:"api"`
	BlockGen  int `mapstructure:"blockgen"  yaml:"blockgen"`
	BlockGRPC int `mapstructure:"blockgrpc" yaml:"blockgrpc"`
	CLI       int `mapstructure:"cli"       yaml:"cli"`
	DID       int `mapstructure:"did"       yaml:"did"`
	Facade    int `mapstructure:"facade"    yaml:"facade"`
	WS        int `mapstructure:"ws"        yaml:"ws"`
	Metrics   int `mapstructure:"metrics"   yaml:"metrics"`
	Profiler  int `mapstructure:"profiler"  yaml:"profiler"`
}

// BindSettings groups all bind address configurations.
// Defaults: Admin ports = 127.0.0.1, Public ports = 0.0.0.0
type BindSettings struct {
	API       string `mapstructure:"api"       yaml:"api"`
	BlockGen  string `mapstructure:"blockgen"  yaml:"blockgen"`
	BlockGRPC string `mapstructure:"blockgrpc" yaml:"blockgrpc"`
	CLI       string `mapstructure:"cli"       yaml:"cli"`
	DID       string `mapstructure:"did"       yaml:"did"`
	Facade    string `mapstructure:"facade"    yaml:"facade"`
	WS        string `mapstructure:"ws"        yaml:"ws"`
	Metrics   string `mapstructure:"metrics"   yaml:"metrics"`
	Profiler  string `mapstructure:"profiler"  yaml:"profiler"`
}

// RedisSettings controls the Redis connection used by the account sync worker.
// The worker uses a Redis Stream (XADD/XREADGROUP/XACK) to decouple the
// WriteAccounts / BatchUpdateAccounts callers from the ~15 s ImmuDB commit latency.
// URL format: "host:port" (e.g. "localhost:6379").
// Env override: JMDN_DATABASE_REDIS_URL, JMDN_DATABASE_REDIS_PASSWORD
type RedisSettings struct {
	URL      string `mapstructure:"url" yaml:"url"`
	Password string `mapstructure:"password" yaml:"password"`
}

// DatabaseSettings controls ImmuDB and Redis connection parameters.
// Env overrides use the JMDN_ prefix (e.g. JMDN_DATABASE_ADDRESS, JMDN_DATABASE_PORT).
type DatabaseSettings struct {
	// ImmuDB connection — override to point at a separate immudb container.
	Address string `mapstructure:"address" yaml:"address"`
	Port    int    `mapstructure:"port"    yaml:"port"`

	Username string        `mapstructure:"username"      yaml:"username"`
	Password string        `mapstructure:"password"      yaml:"password"`
	Redis    RedisSettings `mapstructure:"redis"         yaml:"redis"`

	// TxIndexPath is the path to the SQLite address→tx index file.
	// Defaults to "txindex.db" in the working directory if empty.
	TxIndexPath string `mapstructure:"tx_index_path" yaml:"tx_index_path"`
}

// LoggingSettings mirrors Ion's Config struct so jmdn.yaml can fully configure
// the logger (console, file, OTEL, tracing, metrics) in one place.
// This replaces the old otelconfig.LogConfig and scattered env vars.
type LoggingSettings struct {
	Level       string `mapstructure:"level"        yaml:"level"`
	Development bool   `mapstructure:"development"  yaml:"development"`
	ServiceName string `mapstructure:"service_name" yaml:"service_name"`

	Console LogConsoleSettings `mapstructure:"console" yaml:"console"`
	File    LogFileSettings    `mapstructure:"file"    yaml:"file"`
	OTEL    LogOTELSettings    `mapstructure:"otel"    yaml:"otel"`
	Tracing LogTracingSettings `mapstructure:"tracing" yaml:"tracing"`
}

// LogConsoleSettings controls console (stdout/stderr) output.
type LogConsoleSettings struct {
	Enabled        bool   `mapstructure:"enabled"          yaml:"enabled"`
	Format         string `mapstructure:"format"           yaml:"format"` // json, pretty, systemd
	Color          bool   `mapstructure:"color"            yaml:"color"`
	ErrorsToStderr bool   `mapstructure:"errors_to_stderr" yaml:"errors_to_stderr"`
}

// LogFileSettings controls file output with rotation.
type LogFileSettings struct {
	Enabled    bool   `mapstructure:"enabled"      yaml:"enabled"`
	Path       string `mapstructure:"path"         yaml:"path"`
	MaxSizeMB  int    `mapstructure:"max_size_mb"  yaml:"max_size_mb"`
	MaxAgeDays int    `mapstructure:"max_age_days" yaml:"max_age_days"`
	MaxBackups int    `mapstructure:"max_backups"   yaml:"max_backups"`
	Compress   bool   `mapstructure:"compress"      yaml:"compress"`
}

// LogOTELSettings configures OpenTelemetry log/trace export.
type LogOTELSettings struct {
	Enabled        bool              `mapstructure:"enabled"         yaml:"enabled"`
	Endpoint       string            `mapstructure:"endpoint"        yaml:"endpoint"`
	Protocol       string            `mapstructure:"protocol"        yaml:"protocol"` // grpc or http
	Insecure       bool              `mapstructure:"insecure"        yaml:"insecure"`
	Headers        map[string]string `mapstructure:"headers"         yaml:"headers"`
	Username       string            `mapstructure:"username"        yaml:"username"`
	Password       string            `mapstructure:"password"        yaml:"password"`
	BatchSize      int               `mapstructure:"batch_size"      yaml:"batch_size"`
	ExportInterval time.Duration     `mapstructure:"export_interval" yaml:"export_interval"`
}

// LogTracingSettings configures distributed tracing.
type LogTracingSettings struct {
	Enabled bool   `mapstructure:"enabled" yaml:"enabled"`
	Sampler string `mapstructure:"sampler" yaml:"sampler"` // always, never, ratio:0.5
}

// FeatureSettings toggles optional node features.
type FeatureSettings struct {
	UseLegacyBFT bool `mapstructure:"use_legacy_bft" yaml:"use_legacy_bft"`
	GROTrack     bool `mapstructure:"grotrack"        yaml:"grotrack"`
}

// FastSyncSettings controls FastSync V2 behaviour for this node.
//
// Serving vs syncing are independent:
//   - enabled=true  → this node registers FastSync protocol handlers and serves
//     block/account data to any peer that requests it.
//   - sync=true     → this node is allowed to pull data from peers and update
//     its own local database (HeaderSync, DataSync, Reconciliation).
//
// A sequencer should set sync=false so it never overwrites its own authoritative
// state, while keeping enabled=true so other nodes can still sync from it.
type FastSyncSettings struct {
	// Enabled controls whether the FastSync engine is initialized and protocol
	// handlers are registered. Set false to disable FastSync entirely.
	Enabled bool `mapstructure:"enabled" yaml:"enabled"`

	// EnablePulling controls whether this node pulls data from peers and writes to
	// its local DB (HeaderSync, DataSync, Reconciliation). false = serve-only.
	// Sequencer must keep this false — it is the authoritative source of truth.
	// Also guards all CLI sync commands via PullAllowed.
	EnablePulling bool `mapstructure:"enable_pulling" yaml:"enable_pulling"`

	// EnableCatchup controls whether the SyncMonitor automatically reconciles this
	// node against peers when the seednode reports it is out of sync.
	// Requires enable_pulling=true. Never set on the sequencer.
	EnableCatchup bool `mapstructure:"enable_catchup" yaml:"enable_catchup"`

	// SyncTimeout is the maximum wall-clock time allowed for a single full sync
	// operation before it is cancelled.
	SyncTimeout time.Duration `mapstructure:"sync_timeout" yaml:"sync_timeout"`

	// CatchUpFromBlock is the first block AFTER the bootstrap snapshot
	// (i.e. bootstrapTip + 1). Set this once after loading the bootstrap and
	// never change it. Every catchup run — including after the node goes offline
	// and comes back — scans from this block to remoteTip to find all gaps.
	//
	// 0 = full scan from block 1 (genesis). Use this if no bootstrap was loaded.
	// N = scan from N; bootstrap guaranteed to cover [0..N-1] with no gaps.
	//
	// Do NOT set this to localTip+1: if a previous catchup was partial,
	// localTip may be ahead of gaps that would be silently skipped.
	CatchUpFromBlock uint64 `mapstructure:"catch_up_from_block" yaml:"catch_up_from_block"`

	// SyncCheckInterval is how often the SyncMonitor reports this node's Merkle
	// root to the seednode and checks whether reconciliation is needed.
	// Default: 10 minutes. Minimum enforced by the monitor: 1 minute.
	SyncCheckInterval time.Duration `mapstructure:"sync_check_interval" yaml:"sync_check_interval"`
}
