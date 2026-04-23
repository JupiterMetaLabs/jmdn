package settings

import "time"

// DefaultConfig returns a NodeConfig populated with production-safe defaults.
// These match the current CLI flag defaults in main.go and Ion's Default() config.
func DefaultConfig() NodeConfig {
	return NodeConfig{
		Node: NodeSettings{
			Alias: "",
		},
		Network: NetworkSettings{
			ChainID:           8000800,
			SeedNode:          "",
			Mempool:           "",
			Yggdrasil:         true,
			HeartbeatInterval: 10,
		},
		Ports: PortSettings{
			API:       0, // disabled
			BlockGen:  0, // disabled
			BlockGRPC: 0, // disabled
			CLI:       0, // disabled
			DID:       15052,
			Facade:    8545,
			WS:        8546,

			Metrics:  0, // disabled
			Profiler: 0, // disabled
		},
		Binds: BindSettings{
			API:       "0.0.0.0",   // Public data access
			BlockGen:  "127.0.0.1", // Admin - Block generation
			BlockGRPC: "0.0.0.0",   // P2P - Block propagation
			CLI:       "127.0.0.1", // Admin - CLI control
			DID:       "0.0.0.0",   // Identity Service
			Facade:    "0.0.0.0",   // Public RPC
			WS:        "0.0.0.0",   // Public WS
			Metrics:   "127.0.0.1", // Metrics scraping (usually internal network)
			Profiler:  "127.0.0.1", // Debugging - STRICTLY LOCALHOST
		},
		Database: DatabaseSettings{
			Username: "",
			Password: "",
		},
		Thebe: ThebeConfig{
			Enabled:    false,
			KVPath:     "./data/thebe-kv",
			SQLDSN:     "",
			RedisURL:   "",
			StreamName: "thebedb.events",
			MaxLen:     1000,
			GroupName:  "projector",
		},
		Thebe: ThebeConfig{
			Enabled:    false,
			KVPath:     "./data/thebe-kv",
			SQLDSN:     "",
			RedisURL:   "",
			StreamName: "thebedb.events",
			MaxLen:     1000,
			GroupName:  "projector",
		},
		Logging: LoggingSettings{
			Level:       "warn",
			Development: false,
			ServiceName: "jmdn",
			Console: LogConsoleSettings{
				Enabled:        true,
				Format:         "systemd",
				Color:          true,
				ErrorsToStderr: true,
			},
			File: LogFileSettings{
				Enabled:    false,
				MaxSizeMB:  100,
				MaxAgeDays: 7,
				MaxBackups: 5,
				Compress:   true,
			},
			OTEL: LogOTELSettings{
				Enabled:        false,
				Protocol:       "grpc",
				Insecure:       false,
				BatchSize:      512,
				ExportInterval: 5 * time.Second,
			},
			Tracing: LogTracingSettings{
				Enabled: false,
				Sampler: "ratio:0.2",
			},
		},
		Features: FeatureSettings{
			UseLegacyBFT: false,
			GROTrack:     false,
		},
		FastSync: FastSyncSettings{
			Enabled:      true,
			Sync:         true,
			StartupSync:  true,
			SyncTimeout:  10 * time.Minute,
			AllowedPeers: []string{},
		},
		Security: DefaultSecurityConfig(),
		Alerts:   DefaultAlertsConfig(),
	}
}
