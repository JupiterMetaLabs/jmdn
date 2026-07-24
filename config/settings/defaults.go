package settings

import "time"

// DefaultSelectionSalt is the built-in VRF domain-separation salt for node /
// committee selection. It is NOT secret — it only needs to be identical across
// all nodes on the same network. Replaces the old insecure "test-salt".
const DefaultSelectionSalt = "jmdt-node-selection-v1"

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
			Address:     "localhost", // override via JMDN_DATABASE_ADDRESS for a separate immudb container
			Port:        3322,        // override via JMDN_DATABASE_PORT
			Username:    "immudb",    // immudb built-in default; override via JMDN_DATABASE_USERNAME
			Password:    "immudb",    // immudb built-in default; override via JMDN_DATABASE_PASSWORD
			TxIndexPath: "./DB/txindex.db",
			Redis: RedisSettings{
				URL:      "127.0.0.1:6379", // required for account sync worker; set via jmdn.yaml or JMDN_DATABASE_REDIS_URL
				Password: "jmdnredissync",  // optional: set if Redis requires authentication
			},
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
				Headers:        map[string]string{},
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
			Enabled:           true,
			EnablePulling:     true,
			EnableCatchup:     false,
			SyncTimeout:       10 * time.Minute,
			CatchUpFromBlock:  0,
			SyncCheckInterval: 10 * time.Minute,
		},
		Security: DefaultSecurityConfig(),
		Alerts:   DefaultAlertsConfig(),
		// Selection VRF material:
		//   - Mnemonic is SECRET and has NO default — empty is rejected at use
		//     time (fail-closed) so the insecure public test mnemonic can never
		//     be used implicitly.
		//   - Salt is NOT secret (VRF domain separation) and only needs to be
		//     identical network-wide, so it carries a stable default. Override
		//     per-network via config/env if you want isolation between networks.
		Selection: SelectionSettings{Mnemonic: "", Salt: DefaultSelectionSalt},
		// Consensus policy: empty block_buddy blocklist by default (no peer is
		// manually excluded). Populate via jmdn.yaml or JMDN_CONSENSUS_BLOCK_BUDDY.
		// Committee-source (P1): no pinned authority by default (consumer disabled
		// / fail-closed until an operator pins the seed authority key); epoch clock
		// defaults to the seed's 3600s.
		Consensus: ConsensusSettings{
			BlockBuddy:            nil,
			SeedAuthorityBLSPub:   "",
			CommitteeEpochSeconds: 3600,
			SequencerPeerID:       "",
		},
	}
}
