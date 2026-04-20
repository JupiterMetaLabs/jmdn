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
	Node     NodeSettings     `mapstructure:"node"`
	Network  NetworkSettings  `mapstructure:"network"`
	Ports    PortSettings     `mapstructure:"ports"`
	Binds    BindSettings     `mapstructure:"binds"`
	Database DatabaseSettings `mapstructure:"database"`
	Thebe    ThebeConfig      `mapstructure:"thebe"`
	Logging  LoggingSettings  `mapstructure:"logging"`
	Features FeatureSettings  `mapstructure:"features"`
	Security SecurityConfig   `mapstructure:"security"`
	Alerts   AlertsConfig     `mapstructure:"alerts"`
	FastSyncV2 FastSyncSettings `mapstructure:"fastsyncv2"`
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

// DatabaseSettings controls ImmuDB connection parameters.
type DatabaseSettings struct {
	Username string `mapstructure:"username" yaml:"username"`
	Password string `mapstructure:"password" yaml:"password"`
}

// ThebeConfig controls optional ThebeDB integration.
type ThebeConfig struct {
	Enabled    bool   `mapstructure:"enabled" yaml:"enabled"`         // default false
	KVPath     string `mapstructure:"kv_path" yaml:"kv_path"`         // default "./data/thebe-kv"
	SQLDSN     string `mapstructure:"sql_dsn" yaml:"sql_dsn"`         // reads THEBE_SQL_DSN env var
	RedisURL   string `mapstructure:"redis_url" yaml:"redis_url"`     // optional, reads THEBE_REDIS_URL
	StreamName string `mapstructure:"stream_name" yaml:"stream_name"` // optional, default "jmdt.thebedb.events"
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
	Enabled        bool          `mapstructure:"enabled"         yaml:"enabled"`
	Endpoint       string        `mapstructure:"endpoint"        yaml:"endpoint"`
	Protocol       string        `mapstructure:"protocol"        yaml:"protocol"` // grpc or http
	Insecure       bool          `mapstructure:"insecure"        yaml:"insecure"`
	Username       string        `mapstructure:"username"        yaml:"username"`
	Password       string        `mapstructure:"password"        yaml:"password"`
	BatchSize      int           `mapstructure:"batch_size"      yaml:"batch_size"`
	ExportInterval time.Duration `mapstructure:"export_interval" yaml:"export_interval"`
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

	// Sync controls whether this node will pull data from peers and write to its
	// local DB. false = read-only participant (serves data, never updates itself).
	Sync bool `mapstructure:"sync" yaml:"sync"`

	// StartupSync controls whether the node attempts to catch up on missed blocks
	// automatically when it (re)starts and connects to peers.
	StartupSync bool `mapstructure:"startup_sync" yaml:"startup_sync"`

	// SyncTimeout is the maximum wall-clock time allowed for a single full sync
	// operation before it is cancelled.
	SyncTimeout time.Duration `mapstructure:"sync_timeout" yaml:"sync_timeout"`

	// AllowedPeers is an optional whitelist of libp2p peer IDs this node will
	// accept sync data FROM. Empty list = accept from any peer.
	AllowedPeers []string `mapstructure:"allowed_peers" yaml:"allowed_peers"`
}
