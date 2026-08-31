package settings

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/viper"
)

// globalCfg holds the loaded configuration for package-level access.
var globalCfg *NodeConfig

// Load reads configuration from file, environment, and returns a populated NodeConfig.
// It also stores the config for package-level access via Get().
// Call this once at startup, after CLI flags are parsed.
//
// Config file search paths: ./jmdn.yaml, /etc/jmdn/jmdn.yaml
// Environment prefix: JMDN_ (e.g. JMDN_NODE_CHAIN_ID, JMDN_LOGGING_OTEL_ENDPOINT)
func Load() (*NodeConfig, error) {
	v := viper.New()

	// 1. Set defaults from our DefaultConfig
	setDefaults(v)

	// 4. Config file paths (First found wins)
	// Priority 1: /etc/jmdn/ (System)
	// Priority 2: ./ (Local)
	v.SetConfigName("jmdn")
	v.SetConfigType("yaml")
	v.AddConfigPath("/etc/jmdn/")
	v.AddConfigPath(".")

	// 5. Read config file (optional — not an error if missing)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
		fmt.Println("No configuration file found, using defaults and environment variables")
	} else {
		fmt.Printf("Configuration loaded from: %s\n", v.ConfigFileUsed())
	}

	// 6. Environment variables (Highest priority after flags)
	v.SetEnvPrefix("JMDN")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	// Explicitly support non-prefixed Thebe env vars.
	if err := v.BindEnv("thebe.sql_dsn", "THEBE_SQL_DSN"); err != nil {
		return nil, fmt.Errorf("binding THEBE_SQL_DSN: %w", err)
	}
	if err := v.BindEnv("thebe.redis_url", "THEBE_REDIS_URL"); err != nil {
		return nil, fmt.Errorf("binding THEBE_REDIS_URL: %w", err)
	}
	if err := v.BindEnv("thebe.stream_name", "THEBE_STREAM_NAME"); err != nil {
		return nil, fmt.Errorf("binding THEBE_STREAM_NAME: %w", err)
	}
	if err := v.BindEnv("thebe.max_len", "THEBE_MAX_LEN"); err != nil {
		return nil, fmt.Errorf("binding THEBE_MAX_LEN: %w", err)
	}
	if err := v.BindEnv("thebe.group_name", "THEBE_GROUP_NAME"); err != nil {
		return nil, fmt.Errorf("binding THEBE_GROUP_NAME: %w", err)
	}

	// Bind the selection secrets to their explicit, documented env var names
	// (these differ from the auto-derived JMDN_SELECTION_* names, so bind them
	// directly). BindEnv with explicit names bypasses the prefix.
	_ = v.BindEnv("selection.mnemonic", "JMDN_NODE_SELECTION_MNEMONIC")
	_ = v.BindEnv("selection.salt", "JMDN_NETWORK_SALT")
	// AutomaticEnv does not reach these through Unmarshal — bind explicitly so
	// JMDN_SECURITY_ENABLED=false actually disables the gatekeeper (TLS/auth). Without
	// this, services stay TLS even when the env/partial-yaml says disabled.
	_ = v.BindEnv("security.enabled", "JMDN_SECURITY_ENABLED")

	// AutomaticEnv does not reach nested keys through Unmarshal, so every
	// tx_status key is bound explicitly — otherwise JMDN_TX_STATUS_ENABLED=true
	// would silently do nothing and the feature would appear broken rather than
	// off. Same reason as the security.enabled bind above.
	for key, env := range map[string]string{
		"tx_status.enabled":                   "JMDN_TX_STATUS_ENABLED",
		"tx_status.submit_record_ttl":         "JMDN_TX_STATUS_SUBMIT_RECORD_TTL",
		"tx_status.submit_record_capacity":    "JMDN_TX_STATUS_SUBMIT_RECORD_CAPACITY",
		"tx_status.mempool_timeout":           "JMDN_TX_STATUS_MEMPOOL_TIMEOUT",
		"tx_status.chain_timeout":             "JMDN_TX_STATUS_CHAIN_TIMEOUT",
		"tx_status.negative_cache_ttl":        "JMDN_TX_STATUS_NEGATIVE_CACHE_TTL",
		"tx_status.negative_cache_size":       "JMDN_TX_STATUS_NEGATIVE_CACHE_SIZE",
		"tx_status.rate_limit_per_sec":        "JMDN_TX_STATUS_RATE_LIMIT_PER_SEC",
		"tx_status.rate_limit_burst":          "JMDN_TX_STATUS_RATE_LIMIT_BURST",
		"tx_status.breaker_failure_threshold": "JMDN_TX_STATUS_BREAKER_FAILURE_THRESHOLD",
		"tx_status.breaker_cooldown":          "JMDN_TX_STATUS_BREAKER_COOLDOWN",
		"tx_status.pending_tx_by_hash":        "JMDN_TX_STATUS_PENDING_TX_BY_HASH",
		// Chain-head checkpoint feature (default-off). Bound explicitly for the
		// same reason as tx_status/security: AutomaticEnv does not reach nested
		// keys through Unmarshal, so JMDN_CHECKPOINT_ENABLED=true would silently
		// do nothing otherwise.
		"checkpoint.enabled":          "JMDN_CHECKPOINT_ENABLED",
		"checkpoint.boot_fail_closed": "JMDN_CHECKPOINT_BOOT_FAIL_CLOSED",
		"checkpoint.cadence_blocks":   "JMDN_CHECKPOINT_CADENCE_BLOCKS",
		// Buddy staking rewards (default-off). Same nested-key reason.
		"consensus.reward_address":       "JMDN_CONSENSUS_REWARD_ADDRESS",
		"consensus.reward_split_enabled": "JMDN_CONSENSUS_REWARD_SPLIT_ENABLED",
		// Authenticated committee snapshot source. Required by the SEQUENCER to
		// wire the reward-address source (Sequencer/consensus_statemachine.go gates
		// both eligibility and reward wiring on seed_authority_bls_pub != "" &&
		// seednode != ""). Without these, reward_split_enabled fails every block
		// closed ("reward-address source not configured"). Same nested-key reason
		// as above — AutomaticEnv does not reach nested keys through Unmarshal.
		"consensus.seed_authority_bls_pub": "JMDN_CONSENSUS_SEED_AUTHORITY_BLS_PUB",
		"network.seednode":                 "JMDN_NETWORK_SEEDNODE",
	} {
		if err := v.BindEnv(key, env); err != nil {
			return nil, fmt.Errorf("binding %s: %w", env, err)
		}
	}

	// 6. Unmarshal into struct
	cfg := DefaultConfig()
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}
	normalizeThebeConfig(&cfg)

	// 7. Generic Map Merge for Services
	// Fix Viper's map unmarshaling bug: it replaces map values entirely instead of deep merging.
	// We use reflection to generically merge any zero-valued fields in the user config
	// with the values from the default config.
	defaultCfg := DefaultConfig()
	if cfg.Security.Services == nil {
		cfg.Security.Services = make(map[string]Policy)
	}
	for svcName, defaultPolicy := range defaultCfg.Security.Services {
		userPolicy, exists := cfg.Security.Services[svcName]
		if exists {
			cfg.Security.Services[svcName] = mergeStructs(userPolicy, defaultPolicy)
		} else {
			cfg.Security.Services[svcName] = defaultPolicy
		}
	}

	// Eagerly resolve token env vars so the hot path never calls os.Getenv
	cfg.Security.ResolveTokens()

	globalCfg = &cfg
	return &cfg, nil
}

// IsLoaded reports whether Load() has populated the global config. Lets hot
// paths read optional settings without risking the Get() panic when config has
// not been loaded yet (e.g. early init, or tools that never call Load()).
func IsLoaded() bool { return globalCfg != nil }

// Get returns the loaded NodeConfig. Must be called after Load().
// Panics if Load() has not been called — this is intentional to catch
// initialization order bugs at startup, not in production traffic.
func Get() *NodeConfig {
	if globalCfg == nil {
		panic("settings.Get() called before settings.Load()")
	}
	return globalCfg
}

// setDefaults maps DefaultConfig values into viper keys so that
// environment variables and config file merging work correctly.
func setDefaults(v *viper.Viper) {
	d := DefaultConfig()

	// Node
	v.SetDefault("node.alias", d.Node.Alias)

	// Network
	v.SetDefault("network.chain_id", d.Network.ChainID)
	v.SetDefault("network.seednode", d.Network.SeedNode)
	v.SetDefault("network.mempool", d.Network.Mempool)
	v.SetDefault("network.yggdrasil", d.Network.Yggdrasil)
	v.SetDefault("network.heartbeat_interval", d.Network.HeartbeatInterval)

	// Ports
	v.SetDefault("ports.api", d.Ports.API)
	v.SetDefault("ports.blockgen", d.Ports.BlockGen)
	v.SetDefault("ports.blockgrpc", d.Ports.BlockGRPC)
	v.SetDefault("ports.cli", d.Ports.CLI)
	v.SetDefault("ports.did", d.Ports.DID)
	v.SetDefault("ports.facade", d.Ports.Facade)
	v.SetDefault("ports.thebe_debug", d.Ports.ThebeDebug)
	v.SetDefault("ports.ws", d.Ports.WS)
	v.SetDefault("ports.geth", d.Ports.Geth)
	v.SetDefault("ports.smart", d.Ports.Smart)
	v.SetDefault("ports.metrics", d.Ports.Metrics)
	v.SetDefault("ports.profiler", d.Ports.Profiler)

	// Binds
	v.SetDefault("binds.api", d.Binds.API)
	v.SetDefault("binds.blockgen", d.Binds.BlockGen)
	v.SetDefault("binds.blockgrpc", d.Binds.BlockGRPC)
	v.SetDefault("binds.cli", d.Binds.CLI)
	v.SetDefault("binds.did", d.Binds.DID)
	v.SetDefault("binds.facade", d.Binds.Facade)
	v.SetDefault("binds.thebe_debug", d.Binds.ThebeDebug)
	v.SetDefault("binds.ws", d.Binds.WS)
	v.SetDefault("binds.geth", d.Binds.Geth)
	v.SetDefault("binds.smart", d.Binds.Smart)
	v.SetDefault("binds.metrics", d.Binds.Metrics)
	v.SetDefault("binds.profiler", d.Binds.Profiler)

	// Database
	v.SetDefault("database.redis.url", d.Database.Redis.URL)
	v.SetDefault("database.redis.password", d.Database.Redis.Password)
	v.SetDefault("database.tx_index_path", d.Database.TxIndexPath)

	// Thebe
	v.SetDefault("thebe.enabled", d.Thebe.Enabled)
	v.SetDefault("thebe.kv_path", d.Thebe.KVPath)
	v.SetDefault("thebe.sql_dsn", d.Thebe.SQLDSN)
	v.SetDefault("thebe.redis_url", d.Thebe.RedisURL)
	v.SetDefault("thebe.stream_name", d.Thebe.StreamName)
	v.SetDefault("thebe.max_len", d.Thebe.MaxLen)
	v.SetDefault("thebe.group_name", d.Thebe.GroupName)

	// Logging
	v.SetDefault("logging.level", d.Logging.Level)
	v.SetDefault("logging.development", d.Logging.Development)
	v.SetDefault("logging.service_name", d.Logging.ServiceName)

	// Logging > Console
	v.SetDefault("logging.console.enabled", d.Logging.Console.Enabled)
	v.SetDefault("logging.console.format", d.Logging.Console.Format)
	v.SetDefault("logging.console.color", d.Logging.Console.Color)
	v.SetDefault("logging.console.errors_to_stderr", d.Logging.Console.ErrorsToStderr)

	// Logging > File
	v.SetDefault("logging.file.enabled", d.Logging.File.Enabled)
	v.SetDefault("logging.file.path", d.Logging.File.Path)
	v.SetDefault("logging.file.max_size_mb", d.Logging.File.MaxSizeMB)
	v.SetDefault("logging.file.max_age_days", d.Logging.File.MaxAgeDays)
	v.SetDefault("logging.file.max_backups", d.Logging.File.MaxBackups)
	v.SetDefault("logging.file.compress", d.Logging.File.Compress)

	// Logging > OTEL
	v.SetDefault("logging.otel.enabled", d.Logging.OTEL.Enabled)
	v.SetDefault("logging.otel.endpoint", d.Logging.OTEL.Endpoint)
	v.SetDefault("logging.otel.protocol", d.Logging.OTEL.Protocol)
	v.SetDefault("logging.otel.insecure", d.Logging.OTEL.Insecure)
	v.SetDefault("logging.otel.headers", d.Logging.OTEL.Headers)
	v.SetDefault("logging.otel.username", d.Logging.OTEL.Username)
	v.SetDefault("logging.otel.password", d.Logging.OTEL.Password)
	v.SetDefault("logging.otel.batch_size", d.Logging.OTEL.BatchSize)
	v.SetDefault("logging.otel.export_interval", d.Logging.OTEL.ExportInterval)

	// Logging > Tracing
	v.SetDefault("logging.tracing.enabled", d.Logging.Tracing.Enabled)
	v.SetDefault("logging.tracing.sampler", d.Logging.Tracing.Sampler)

	// Features
	v.SetDefault("features.use_legacy_bft", d.Features.UseLegacyBFT)
	v.SetDefault("features.grotrack", d.Features.GROTrack)

	// FastSync
	v.SetDefault("contracts.enabled", d.Contracts.Enabled)
	v.SetDefault("fastsync.enabled", d.FastSync.Enabled)
	v.SetDefault("fastsync.enable_pulling", d.FastSync.EnablePulling)
	v.SetDefault("fastsync.enable_catchup", d.FastSync.EnableCatchup)
	v.SetDefault("fastsync.sync_timeout", d.FastSync.SyncTimeout)
	v.SetDefault("fastsync.catch_up_from_block", d.FastSync.CatchUpFromBlock)
	v.SetDefault("fastsync.sync_check_interval", d.FastSync.SyncCheckInterval)

	// Security
	v.SetDefault("security.enabled", d.Security.Enabled)
	v.SetDefault("security.strict_posture", d.Security.StrictPosture)
	v.SetDefault("security.cert_dir", d.Security.CertDir)
	v.SetDefault("security.ip_cache_size", d.Security.IPCacheSize)
	v.SetDefault("security.global_rate_limit", d.Security.GlobalRateLimit)
	v.SetDefault("security.global_burst", d.Security.GlobalBurst)
	v.SetDefault("security.trust_forwarded_headers", d.Security.TrustForwardedHeaders)
	v.SetDefault("security.trusted_proxies", d.Security.TrustedProxies)
	v.SetDefault("security.trusted_clients", d.Security.TrustedClients)
	v.SetDefault("security.explorer_api_key", d.Security.ExplorerAPIKey)
	v.SetDefault("security.jwt_secret", d.Security.JWTSecret)

	// Register defaults for all predefined Security Services so Viper can pick up ENV overrides
	for svcName, policy := range d.Security.Services {
		prefix := "security.services." + svcName + "."
		v.SetDefault(prefix+"tls", policy.TLS)
		v.SetDefault(prefix+"auth_type", string(policy.AuthType))
		v.SetDefault(prefix+"token_env", policy.TokenEnv)
		v.SetDefault(prefix+"rate_limit", policy.RateLimit)
		v.SetDefault(prefix+"burst", policy.Burst)
		v.SetDefault(prefix+"cert_file", policy.CertFile)
		v.SetDefault(prefix+"key_file", policy.KeyFile)
		v.SetDefault(prefix+"ca_file", policy.CAFile)
	}

	// Selection (VRF key material — no safe default; empty is rejected at use)
	v.SetDefault("selection.mnemonic", d.Selection.Mnemonic)
	v.SetDefault("selection.salt", d.Selection.Salt)

	// Consensus
	v.SetDefault("consensus.block_buddy", d.Consensus.BlockBuddy)
	v.SetDefault("consensus.seed_authority_bls_pub", d.Consensus.SeedAuthorityBLSPub)
	v.SetDefault("consensus.committee_epoch_seconds", d.Consensus.CommitteeEpochSeconds)
	v.SetDefault("consensus.committee_epoch_blocks", d.Consensus.CommitteeEpochBlocks)
	v.SetDefault("consensus.require_pinned_committee", d.Consensus.RequirePinnedCommittee)
	v.SetDefault("consensus.committee_strict_boundary", d.Consensus.CommitteeStrictBoundary)
	v.SetDefault("consensus.max_validators", d.Consensus.MaxValidators)
	v.SetDefault("consensus.p2p", d.Consensus.P2P)
	v.SetDefault("consensus.reward_address", d.Consensus.RewardAddress)
	v.SetDefault("consensus.reward_split_enabled", d.Consensus.RewardSplitEnabled)

	// Alerts
	v.SetDefault("alerts.url", d.Alerts.URL)
	v.SetDefault("alerts.api_key", d.Alerts.APIKey)
	v.SetDefault("alerts.chat_id", d.Alerts.ChatID)
	v.SetDefault("alerts.http_timeout", d.Alerts.HTTPTimeout)

	// Orchestrator callback (consensus-rejection reports)
	v.SetDefault("orchestrator.url", d.Orchestrator.URL)
	v.SetDefault("orchestrator.api_key", d.Orchestrator.APIKey)
	v.SetDefault("orchestrator.http_timeout", d.Orchestrator.HTTPTimeout)
	v.SetDefault("orchestrator.max_attempts", d.Orchestrator.MaxAttempts)

	// Transaction status resolution (default-off)
	v.SetDefault("tx_status.enabled", d.TxStatus.Enabled)
	v.SetDefault("tx_status.submit_record_ttl", d.TxStatus.SubmitRecordTTL)
	v.SetDefault("tx_status.submit_record_capacity", d.TxStatus.SubmitRecordCapacity)
	v.SetDefault("tx_status.mempool_timeout", d.TxStatus.MempoolTimeout)
	v.SetDefault("tx_status.chain_timeout", d.TxStatus.ChainTimeout)
	v.SetDefault("tx_status.negative_cache_ttl", d.TxStatus.NegativeCacheTTL)
	v.SetDefault("tx_status.negative_cache_size", d.TxStatus.NegativeCacheSize)
	v.SetDefault("tx_status.rate_limit_per_sec", d.TxStatus.RateLimitPerSec)
	v.SetDefault("tx_status.rate_limit_burst", d.TxStatus.RateLimitBurst)
	v.SetDefault("tx_status.breaker_failure_threshold", d.TxStatus.BreakerFailureThreshold)
	v.SetDefault("tx_status.breaker_cooldown", d.TxStatus.BreakerCooldown)
	v.SetDefault("tx_status.pending_tx_by_hash", d.TxStatus.PendingTxByHash)

	// Chain-head checkpoint (committee-signed anchor) — default-off.
	v.SetDefault("checkpoint.enabled", d.Checkpoint.Enabled)
	v.SetDefault("checkpoint.boot_fail_closed", d.Checkpoint.BootFailClosed)
	v.SetDefault("checkpoint.cadence_blocks", d.Checkpoint.CadenceBlocks)
}

// mergeStructs merges src into dest generically.
// If a field in dest is its zero value, it takes the value from src.
func mergeStructs[T any](dest, src T) T {
	vDest := reflect.ValueOf(&dest).Elem()
	vSrc := reflect.ValueOf(src)

	for i := 0; i < vDest.NumField(); i++ {
		field := vDest.Field(i)
		if field.CanSet() && field.IsZero() {
			field.Set(vSrc.Field(i))
		}
	}
	return dest
}

func normalizeThebeConfig(cfg *NodeConfig) {
	if cfg == nil {
		return
	}
	cfg.Thebe.StreamName = strings.TrimSpace(cfg.Thebe.StreamName)
	if cfg.Thebe.StreamName == "" {
		cfg.Thebe.StreamName = "thebedb.events"
	}
	cfg.Thebe.GroupName = strings.TrimSpace(cfg.Thebe.GroupName)
	if cfg.Thebe.GroupName == "" {
		cfg.Thebe.GroupName = "projector"
	}
	if cfg.Thebe.MaxLen <= 0 {
		cfg.Thebe.MaxLen = 1000
	}
}
