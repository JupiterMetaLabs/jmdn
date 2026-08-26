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
			API:        0, // disabled
			BlockGen:   0, // disabled
			BlockGRPC:  0, // disabled
			CLI:        0, // disabled
			DID:        15052,
			Facade:     8545,
			ThebeDebug: 19090,
			WS:         8546,
			Geth:       15054,
			Smart:      15056,

			Metrics:  0, // disabled
			Profiler: 0, // disabled
		},
		Binds: BindSettings{
			API:        "0.0.0.0",   // Public data access
			BlockGen:   "127.0.0.1", // Admin - Block generation
			BlockGRPC:  "0.0.0.0",   // P2P - Block propagation
			CLI:        "127.0.0.1", // Admin - CLI control
			DID:        "0.0.0.0",   // Identity Service
			Facade:     "0.0.0.0",   // Public RPC
			ThebeDebug: "127.0.0.1", // Internal debug APIs
			WS:         "0.0.0.0",   // Public WS
			Geth:       "127.0.0.1", // Internal gRPC
			Smart:      "127.0.0.1", // Internal gRPC
			Metrics:    "127.0.0.1", // Metrics scraping (usually internal network)
			Profiler:   "127.0.0.1", // Debugging - STRICTLY LOCALHOST
		},
		Database: DatabaseSettings{
			TxIndexPath: "./DB/txindex.db",
			Redis: RedisSettings{
				URL: "127.0.0.1:6379", // required for account sync worker; set via jmdn.yaml or JMDN_DATABASE_REDIS_URL
				// SEC-04: no baked default secret. Set via jmdn.yaml or
				// JMDN_DATABASE_REDIS_PASSWORD to match the Redis --requirepass.
				Password: "",
			},
		},
		Thebe: ThebeConfig{
			// ThebeDB is the node's only storage backend post ImmuDB removal —
			// enabled by default. DSN matches the Postgres provisioned by
			// Scripts/install_services.sh / setup_postgres.sh (host port 5430).
			Enabled: true,
			KVPath:  "./storage/thebe-kv",
			// SEC-04: require TLS by default (no cleartext SQL transport). The
			// Postgres this points at MUST have TLS enabled; for a non-TLS local/
			// dev DB, override THEBE_SQL_DSN with sslmode=disable explicitly.
			SQLDSN:     "postgres://jmdn:jmdndefault@127.0.0.1:5430/jmdn?sslmode=require",
			RedisURL:   "",
			StreamName: "",
			MaxLen:     1000,
			GroupName:  "",
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
			// DISABLED pending the ThebeDB FastSync redesign (log-shipping model).
			// The current ImmuDB-era protocol is turned off fleet-wide; flip back
			// to true when the new engine lands. Serving + syncing + SyncMonitor
			// are all gated by this one flag.
			Enabled:           false,
			EnablePulling:     true,
			EnableCatchup:     false,
			SyncTimeout:       10 * time.Minute,
			CatchUpFromBlock:  0,
			SyncCheckInterval: 10 * time.Minute,
		},
		Security: DefaultSecurityConfig(),
		Alerts:   DefaultAlertsConfig(),
		// Consensus-rejection reporting to the orchestrator: disabled until
		// an operator sets orchestrator.url + orchestrator.api_key.
		Orchestrator: DefaultOrchestratorConfig(),
		// Selection VRF material:
		//   - Mnemonic is SECRET and has NO default — empty is rejected at use
		//     time (fail-closed) so the insecure public test mnemonic can never
		//     be used implicitly.
		//   - Salt is NOT secret (VRF domain separation) and only needs to be
		//     identical network-wide, so it carries a stable default. Override
		//     per-network via config/env if you want isolation between networks.
		Selection: SelectionSettings{Mnemonic: "", Salt: DefaultSelectionSalt},
		// Contract execution ENABLED by default. The cycle-5 blockers that drove the
		// temporary default-off have landed: deploy double-nonce fixed (single-use
		// deploy gone), fingerprint reader is node-independent (ORDER BY LOWER(address)),
		// tx.GasLimit is bounded on ingress (config.MaxTxGasLimit), non-deterministic
		// state-read/commit errors fail the block closed (not a silent revert), and
		// receipts persist via the gateway 2PC path. With this ON, contract txs execute
		// during apply and the P2.5 HALT-on-divergence is fleet-wide — so run a
		// HOMOGENEOUS fleet: a heterogeneously-flagged or EVM-less member will fork.
		// Residual (tracked): NEW-2 commit-before-fold — a successful exec commits
		// contract state before FoldContractExecution, with no undo on a fold reject.
		Contracts: ContractsSettings{Enabled: true},
		// Consensus policy: empty block_buddy blocklist by default (no peer is
		// manually excluded). Populate via jmdn.yaml or JMDN_CONSENSUS_BLOCK_BUDDY.
		// Committee-source: no pinned authority by default (consumer disabled
		// / fail-closed until an operator pins the seed authority key); epoch clock
		// defaults to the seed's 3600s.
		Consensus: ConsensusSettings{
			BlockBuddy:            nil,
			SeedAuthorityBLSPub:   "",
			CommitteeEpochSeconds: 3600,
			MaxValidators:         7, // must match config.MaxMainPeers (the voting committee size); never 0
			P2P:                   1, // 1 = direct p2p + gossip (default, resilient); set 0 for gossip-only
		},
		// Transaction-status resolution: DEFAULT-OFF, so the RPC surface behaves
		// exactly as it does today until an operator opts in. The numbers below
		// only take effect once Enabled=true.
		TxStatus: TxStatusSettings{
			Enabled: false,
			// 30m is a placeholder, not a measurement. The sequencer polls the
			// mempool on an interval and only builds a block once enough
			// transactions are pending, so real worst-case inclusion must be
			// measured on the target network before this is trusted. Too short
			// and an in-flight transaction reports `unknown`; too long and a
			// dropped transaction reports `processing` for longer than it should.
			SubmitRecordTTL:      30 * time.Minute,
			SubmitRecordCapacity: 100_000,
			// Small on purpose: this bounds how long a status query can hold an
			// RPC handler waiting on the mempool. Expiry degrades to `unknown`.
			MempoolTimeout: 400 * time.Millisecond,
			ChainTimeout:   2 * time.Second,
			// Short TTL: only CONCLUSIVE unknowns are cached, but a hash
			// submitted moments after a miss must not stay invisible for long.
			NegativeCacheTTL:  2 * time.Second,
			NegativeCacheSize: 16_384,
			// Load protection for the mempool fleet, not tuning — the JSON-RPC
			// port is public and each chain-store miss amplifies into a
			// fleet-wide fan-out.
			RateLimitPerSec:         50,
			RateLimitBurst:          100,
			BreakerFailureThreshold: 5,
			BreakerCooldown:         5 * time.Second,
			// Off even when the feature is on: serving pending transactions from
			// eth_getTransactionByHash changes what an existing client sees, so
			// it is a second, separate opt-in.
			PendingTxByHash: false,
		},
	}
}
