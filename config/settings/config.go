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
	Thebe        ThebeConfig        `mapstructure:"thebe"`
	Logging      LoggingSettings    `mapstructure:"logging"`
	Features     FeatureSettings    `mapstructure:"features"`
	Security     SecurityConfig     `mapstructure:"security"`
	Alerts       AlertsConfig       `mapstructure:"alerts"`
	Orchestrator OrchestratorConfig `mapstructure:"orchestrator"`
	FastSync     FastSyncSettings   `mapstructure:"fastsync"`
	Selection    SelectionSettings  `mapstructure:"selection"`
	Consensus    ConsensusSettings  `mapstructure:"consensus"`
	Contracts    ContractsSettings  `mapstructure:"contracts"`
	TxStatus     TxStatusSettings   `mapstructure:"tx_status"`
}

// TxStatusSettings controls transaction-status resolution for transactions that
// are not in a block: the local submit log, the MRE mempool lookup, and the
// jmdt_getTransactionStatus RPC.
//
// Disabled by default. With Enabled=false:
//   - no submit records are written,
//   - jmdt_getTransactionStatus reports the feature as disabled,
//   - eth_getTransactionByHash behaves exactly as before (an error for a hash
//     that is not in a block).
//
// YAML:
//
//	tx_status:
//	  enabled: true
//	  submit_record_ttl: 30m
//
// Env: JMDN_TX_STATUS_ENABLED, JMDN_TX_STATUS_SUBMIT_RECORD_TTL, …
type TxStatusSettings struct {
	// Enabled turns the whole feature on. Default false.
	Enabled bool `mapstructure:"enabled" yaml:"enabled"`

	// SubmitRecordTTL is how long a local submit record is kept. It must
	// comfortably exceed the worst-case time-to-inclusion, or an in-flight
	// transaction will report `unknown` instead of `processing` before it is
	// mined. The sequencer polls the mempool on an interval and only builds a
	// block once enough transactions are pending, so worst-case inclusion is
	// far longer than intuition suggests — MEASURE it rather than trusting this
	// default.
	SubmitRecordTTL time.Duration `mapstructure:"submit_record_ttl" yaml:"submit_record_ttl"`
	// SubmitRecordCapacity bounds the in-memory submit log.
	SubmitRecordCapacity int `mapstructure:"submit_record_capacity" yaml:"submit_record_capacity"`

	// MempoolTimeout bounds one MRE lookup. A status query must never block an
	// RPC handler, so this stays small and expiry degrades to `unknown`.
	MempoolTimeout time.Duration `mapstructure:"mempool_timeout" yaml:"mempool_timeout"`
	// ChainTimeout bounds each chain-store read.
	ChainTimeout time.Duration `mapstructure:"chain_timeout" yaml:"chain_timeout"`

	// NegativeCacheTTL / NegativeCacheSize remember CONCLUSIVE unknowns so a
	// burst of probes for nonexistent hashes does not become a burst of
	// fleet-wide mempool scatter-gathers. Inconclusive answers are never
	// cached. Keep the TTL short: a transaction submitted moments after a miss
	// must not stay invisible.
	NegativeCacheTTL  time.Duration `mapstructure:"negative_cache_ttl" yaml:"negative_cache_ttl"`
	NegativeCacheSize int           `mapstructure:"negative_cache_size" yaml:"negative_cache_size"`

	// RateLimitPerSec / RateLimitBurst cap the sustained status-lookup rate.
	// This is load protection for the mempool fleet, not tuning: the JSON-RPC
	// port is public and each miss amplifies into an N-shard fan-out.
	RateLimitPerSec float64 `mapstructure:"rate_limit_per_sec" yaml:"rate_limit_per_sec"`
	RateLimitBurst  int     `mapstructure:"rate_limit_burst" yaml:"rate_limit_burst"`

	// BreakerFailureThreshold / BreakerCooldown stop calling an unresponsive
	// mempool, so an outage does not add the full timeout to every request.
	BreakerFailureThreshold int           `mapstructure:"breaker_failure_threshold" yaml:"breaker_failure_threshold"`
	BreakerCooldown         time.Duration `mapstructure:"breaker_cooldown" yaml:"breaker_cooldown"`

	// PendingTxByHash controls whether eth_getTransactionByHash may answer from
	// the mempool. When true a queued transaction is returned with null
	// blockHash/blockNumber/transactionIndex (the standard Ethereum pending
	// representation) and an unknown hash returns null instead of an error.
	// When false eth_getTransactionByHash is untouched.
	//
	// eth_getTransactionReceipt is NOT affected by this or any other setting
	// here: a receipt must stay null until the transaction is in a block,
	// because wallets read a non-null receipt as proof of mining and a
	// synthesised status:0x0 renders as a FAILED transaction.
	PendingTxByHash bool `mapstructure:"pending_tx_by_hash" yaml:"pending_tx_by_hash"`
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
	//
	// WALL CLOCK. It selects which committee SNAPSHOT the seed node serves. It
	// must never be used to derive a selection seed - see CommitteeEpochBlocks.
	CommitteeEpochSeconds int64 `mapstructure:"committee_epoch_seconds" yaml:"committee_epoch_seconds"`

	// CommitteeEpochBlocks is the selection-epoch length in BLOCKS, used by
	// messaging.EpochForHeight to map a height to the epoch that is hashed into
	// the committee seed.
	//
	// This is consensus-critical and must be identical network-wide: two nodes
	// with different values derive different seeds for the same block, seat
	// different committees, and reject each other's certificates. Change it only
	// as a coordinated fleet-wide flip.
	//
	// 0 (the default) means "one epoch" - every height maps to epoch 0. That is
	// correct for Stage 1, where the seed source is a fixed salt that ignores the
	// epoch and the draw already rotates on height and prev_hash. Stage 2
	// (RANDAO + VDF) keys its beacon on this epoch and needs a real value.
	CommitteeEpochBlocks int64 `mapstructure:"committee_epoch_blocks" yaml:"committee_epoch_blocks"`

	// RequirePinnedCommittee makes committee selection resolve its candidate pool
	// from the FROZEN snapshot of the block's selection epoch, instead of reading
	// the current one live (W1 pool pinning).
	//
	// Unpinned, the seed is derived from the block but the pool is not: two nodes
	// resolving the pool either side of a membership change seat different
	// committees and compute different n, hence different T = ceil(2n/3). Live
	// that is a split; retroactively it means a synced node cannot re-derive the
	// committee that already voted on an old block.
	//
	// FAIL CLOSED: with this set, an eligibility source that cannot serve a
	// specific epoch returns messaging.ErrCommitteeNotPinned and the round is
	// refused. Do NOT enable until the seed node can serve GetCommitteeSnapshot
	// for a PAST epoch, or jmdn persists each epoch's snapshot with its signature.
	//
	// Also requires committee_epoch_blocks to be non-zero — at 0 every height maps
	// to epoch 0, so "pin per epoch" pins all history to a single snapshot.
	//
	// Default false = today's behaviour, unchanged.
	RequirePinnedCommittee bool `mapstructure:"require_pinned_committee" yaml:"require_pinned_committee"`

	// CommitteeStrictBoundary stops a node bridging an epoch CHANGE with a cached
	// committee snapshot when the seed is unreachable.
	//
	// The snapshot freshness window is ±1 epoch, so just after an epoch boundary a
	// node serving its cached previous-epoch set still passes freshness while a
	// node that fetched successfully uses the new set — different sets, different
	// n, different T, and each believes it is right. Bridging WITHIN an epoch is
	// unaffected; that is what the cache is for.
	//
	// Trade: a stalled node rejoins cleanly, a split node does not. Becomes
	// important once membership actually rotates per epoch.
	//
	// Default false = today's behaviour, unchanged.
	CommitteeStrictBoundary bool `mapstructure:"committee_strict_boundary" yaml:"committee_strict_boundary"`

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

// ContractsSettings controls the smart-contract / EVM execution layer.
//
// Enabled wires an execbridge.ContractExecutor into the block-apply path so
// contract (Type-2 / deployment) transactions are executed during consensus
// apply instead of falling through to a plain value transfer. Default TRUE
// (operator decision 2026-08-19). With it on, contract txs execute during apply
// and the P2.5 state-fingerprint HALT-on-divergence is active.
// PRECONDITIONS the operator MUST ensure before running with this on:
//   - the 2-node determinism gate has passed (deploy/call/payable → identical
//     fingerprint/balances/receipts; a diverged node halts);
//   - the fleet is homogeneous — every consensus node runs this binary with the
//     flag on. A heterogeneously-flagged fleet, or any EVM-less (old) node,
//     forks/breaks on contract blocks (some execute, some transfer). Set to false
//     in jmdn.yaml to run a node dormant.
type ContractsSettings struct {
	Enabled bool `mapstructure:"enabled" yaml:"enabled"`
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

	// Environment names which network this node is part of: "mainnet" or
	// "testnet". This is a SAFETY GATE, not just metadata — Features.AvcValidation
	// (the avc consensus-adapter rollout) refuses to run unless this is exactly
	// "testnet", regardless of the AvcValidation.Enabled flag. Defaults to
	// "mainnet" (see DefaultConfig) so an operator who never sets this field gets
	// the safe behavior: the new validation path stays off everywhere until
	// explicitly opted into on a testnet node. Empty or any other value is
	// treated as "not testnet" (fail-closed), never as an implicit yes.
	Environment string `mapstructure:"environment" yaml:"environment"`
}

// PortSettings groups all port/address assignments.
type PortSettings struct {
	API        int `mapstructure:"api"       yaml:"api"`
	BlockGen   int `mapstructure:"blockgen"  yaml:"blockgen"`
	BlockGRPC  int `mapstructure:"blockgrpc" yaml:"blockgrpc"`
	CLI        int `mapstructure:"cli"       yaml:"cli"`
	DID        int `mapstructure:"did"       yaml:"did"`
	Facade     int `mapstructure:"facade"    yaml:"facade"`
	ThebeDebug int `mapstructure:"thebe_debug" yaml:"thebe_debug"`
	WS         int `mapstructure:"ws"        yaml:"ws"`
	Geth       int `mapstructure:"geth"      yaml:"geth"`
	Smart      int `mapstructure:"smart"     yaml:"smart"`
	Metrics    int `mapstructure:"metrics"   yaml:"metrics"`
	Profiler   int `mapstructure:"profiler"  yaml:"profiler"`
}

// BindSettings groups all bind address configurations.
// Defaults: Admin ports = 127.0.0.1, Public ports = 0.0.0.0
type BindSettings struct {
	API        string `mapstructure:"api"       yaml:"api"`
	BlockGen   string `mapstructure:"blockgen"  yaml:"blockgen"`
	BlockGRPC  string `mapstructure:"blockgrpc" yaml:"blockgrpc"`
	CLI        string `mapstructure:"cli"       yaml:"cli"`
	DID        string `mapstructure:"did"       yaml:"did"`
	Facade     string `mapstructure:"facade"    yaml:"facade"`
	ThebeDebug string `mapstructure:"thebe_debug" yaml:"thebe_debug"`
	WS         string `mapstructure:"ws"        yaml:"ws"`
	Geth       string `mapstructure:"geth"      yaml:"geth"`
	Smart      string `mapstructure:"smart"     yaml:"smart"`
	Metrics    string `mapstructure:"metrics"   yaml:"metrics"`
	Profiler   string `mapstructure:"profiler"  yaml:"profiler"`
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

// ThebeConfig controls the ThebeDB storage backend (the node's only DB).
type ThebeConfig struct {
	Enabled    bool           `mapstructure:"enabled" yaml:"enabled"`         // default true — ThebeDB is the only storage backend
	KVPath     string         `mapstructure:"kv_path" yaml:"kv_path"`         // default "./data/thebe-kv"
	SQLDSN     string         `mapstructure:"sql_dsn" yaml:"sql_dsn"`         // reads THEBE_SQL_DSN env var
	RedisURL   string         `mapstructure:"redis_url" yaml:"redis_url"`     // optional, reads THEBE_REDIS_URL
	StreamName string         `mapstructure:"stream_name" yaml:"stream_name"` // optional, default "thebedb.events"
	MaxLen     int64          `mapstructure:"max_len" yaml:"max_len"`         // optional, default 1000
	GroupName  string         `mapstructure:"group_name" yaml:"group_name"`   // optional, default "projector"
	CDC        ThebeCDCConfig `mapstructure:"cdc" yaml:"cdc"`
}

type ThebeCDCConfig struct {
	Enabled     bool   `mapstructure:"enabled" yaml:"enabled"`
	SlotName    string `mapstructure:"slot_name" yaml:"slot_name"`
	Publication string `mapstructure:"publication" yaml:"publication"`
	LogPath     string `mapstructure:"log_path" yaml:"log_path"`
	DLQPath     string `mapstructure:"dlq_path" yaml:"dlq_path"`
	MaxLagBytes int64  `mapstructure:"max_lag_bytes" yaml:"max_lag_bytes"`
}

// DatabaseSettings controls ImmuDB and Redis connection parameters.
// Env overrides use the JMDN_ prefix (e.g. JMDN_DATABASE_ADDRESS, JMDN_DATABASE_PORT).
type DatabaseSettings struct {
	Redis RedisSettings `mapstructure:"redis" yaml:"redis"`

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

	// AvcValidation controls the staged rollout of the avc-based consensus
	// validator (consensus/adapters) alongside jmdn's existing
	// Security.CheckZKBlockValidation. See AvcValidationSettings for the
	// mode semantics and NetworkSettings.Environment for the testnet-only gate.
	AvcValidation AvcValidationSettings `mapstructure:"avc_validation" yaml:"avc_validation"`
}

// AvcValidationSettings controls the staged rollout described in the A3
// adapter work: shadow mode (compare, don't act) -> feature-flagged enforce
// (per-node opt-in) -> full cutover (flip the default). Both stages are the
// SAME code path here; "full cutover" is simply flipping this config's
// default in DefaultConfig() once shadow mode has run clean for the agreed
// period — no further code change needed for that step.
//
// SAFETY: even with Enabled=true, the validator only runs when
// NetworkSettings.Environment == "testnet" (see EvaluateShadow in
// consensus/adapters/shadow.go). This lets ops enable it per-node via yaml
// without a redeploy, restricted to testnet nodes only, exactly matching the
// "gradually enable on a few validators, testnet only" rollout plan.
type AvcValidationSettings struct {
	// Enabled is the master per-node switch. Default false — opt-in only.
	Enabled bool `mapstructure:"enabled" yaml:"enabled"`

	// Mode is "shadow" (default/safe: run the new validator, log any
	// disagreement with the legacy decision, but the legacy decision still
	// determines the actual vote) or "enforce" (the new validator's verdict
	// BECOMES the vote decision; an internal error in this mode fails closed
	// — rejects the block — rather than silently falling back to legacy).
	// Any value other than "enforce" (including empty/unrecognized) is
	// treated as "shadow" — the safe default.
	Mode string `mapstructure:"mode" yaml:"mode"`
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

	// ServeLegacy registers the retired FastsyncV2 (/fastsync/v1 Merkle-bisection)
	// SERVING handlers so legacy ImmuDB + old-FastSync nodes can still sync. Set
	// TRUE only on the sequencer (the sole authoritative source legacy nodes pull
	// from); new ThebeDB nodes use ThebeSync (/fastsync/v4) and leave this false.
	// Independent of Enabled/EnablePulling — it is serve-only and never pulls.
	ServeLegacy bool `mapstructure:"serve_legacy" yaml:"serve_legacy"`

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
