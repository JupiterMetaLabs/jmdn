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
	v.SetDefault("binds.ws", d.Binds.WS)
	v.SetDefault("binds.geth", d.Binds.Geth)
	v.SetDefault("binds.smart", d.Binds.Smart)
	v.SetDefault("binds.metrics", d.Binds.Metrics)
	v.SetDefault("binds.profiler", d.Binds.Profiler)

	// Database
	v.SetDefault("database.username", d.Database.Username)
	v.SetDefault("database.password", d.Database.Password)

	// Thebe
	v.SetDefault("thebe.enabled", d.Thebe.Enabled)
	v.SetDefault("thebe.kv_path", d.Thebe.KVPath)
	v.SetDefault("thebe.sql_dsn", d.Thebe.SQLDSN)
	v.SetDefault("thebe.redis_url", d.Thebe.RedisURL)
	v.SetDefault("thebe.stream_name", d.Thebe.StreamName)
	v.SetDefault("thebe.max_len", d.Thebe.MaxLen)
	v.SetDefault("thebe.group_name", d.Thebe.GroupName)

	// ContractDB
	v.SetDefault("contractdb.backend", d.ContractDB.Backend)

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
	v.SetDefault("fastsync.enabled", d.FastSync.Enabled)
	v.SetDefault("fastsync.sync", d.FastSync.Sync)
	v.SetDefault("fastsync.startup_sync", d.FastSync.StartupSync)
	v.SetDefault("fastsync.sync_timeout", d.FastSync.SyncTimeout)
	v.SetDefault("fastsync.allowed_peers", d.FastSync.AllowedPeers)

	// Security
	v.SetDefault("security.explorer_api_key", d.Security.ExplorerAPIKey)
	v.SetDefault("security.jwt_secret", d.Security.JWTSecret)

	// Alerts
	v.SetDefault("alerts.url", d.Alerts.URL)
	v.SetDefault("alerts.api_key", d.Alerts.APIKey)
	v.SetDefault("alerts.chat_id", d.Alerts.ChatID)
	v.SetDefault("alerts.http_timeout", d.Alerts.HTTPTimeout)
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
