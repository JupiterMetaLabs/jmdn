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
			// Fail-safe default: an operator who never sets this gets "mainnet",
			// which keeps Features.AvcValidation off regardless of its own
			// Enabled flag (see AvcValidationSettings doc comment).
			Environment: "mainnet",
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
				URL:      "127.0.0.1:6379", // required for account sync worker; set via jmdn.yaml or JMDN_DATABASE_REDIS_URL
				Password: "jmdnredissync",  // optional: set if Redis requires authentication
			},
		},
		Thebe: ThebeConfig{
			// ThebeDB is the node's only storage backend post ImmuDB removal —
			// enabled by default. DSN matches the Postgres provisioned by
			// Scripts/install_services.sh / setup_postgres.sh (host port 5430).
			Enabled:    true,
			KVPath:     "./storage/thebe-kv",
			SQLDSN:     "postgres://jmdn:jmdndefault@127.0.0.1:5430/jmdn?sslmode=disable",
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
			// Off by default everywhere; opt in per-node via yaml, and only takes
			// effect on a node whose Network.Environment is "testnet".
			AvcValidation: AvcValidationSettings{
				Enabled: false,
				Mode:    "shadow",
			},
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
		// Consensus policy: empty block_buddy blocklist by default (no peer is
		// manually excluded). Populate via jmdn.yaml or JMDN_CONSENSUS_BLOCK_BUDDY.
		// Committee-source: no pinned authority by default (consumer disabled
		// / fail-closed until an operator pins the seed authority key); epoch clock
		// defaults to the seed's 3600s.
		Consensus: ConsensusSettings{
			BlockBuddy:            nil,
			SeedAuthorityBLSPub:   "",
			CommitteeEpochSeconds: 3600,
			// Selection-epoch length in BLOCKS (messaging.EpochForHeight).
			//
			// SET TO 1 (2026-08-27, operator decision): every height is its own
			// selection epoch, so the buddy-selection validator pool is frozen
			// PER BLOCK rather than per multi-block epoch. A validator that
			// registers between block N and N+1 is eligible for N+1's draw
			// instead of waiting for an epoch boundary. Consensus-critical and
			// must be identical network-wide - see config.go.
			//
			// !! REVISIT BEFORE STAGE-2 (RANDAO+VDF) BEACON INSTALL !!
			// config.go's own contract for this field is "Stage 2 keys its
			// beacon on this epoch", and messaging.SeedSourceFor does
			// beacon.Has(epoch) with THIS epoch value. At 1, epoch == height,
			// so a beacon that stores entropy under 50-slot entropy epochs
			// (messaging.EpochForSlot, N=50) will miss on essentially every
			// lookup and silently fall back to SaltSource - the wrong-entropy
			// bug, not a loud failure. The fix is to split committee.SeedInput
			// into EntropyEpoch (slot-based) and the selection period
			// (block-based); until that lands, 1 is only safe while the beacon
			// is NOT installed (i.e. JMDN_AVC_VDF_MODULUS_HEX /
			// JMDN_AVC_VDF_DIFFICULTY_T unset - see Sequencer.InstallAVCBeaconFromEnv).
			CommitteeEpochBlocks: 1,
			// W1 pool pinning: OFF. Needs a source that can serve a past epoch,
			// and a non-zero committee_epoch_blocks. See config.go.
			RequirePinnedCommittee: false,
			// Boundary bridging: permissive, as today. See config.go.
			CommitteeStrictBoundary: false,
			MaxValidators:           7, // must match config.MaxMainPeers (the voting committee size); never 0
			P2P:                     1, // 1 = direct p2p + gossip (default, resilient); set 0 for gossip-only
		},
	}
}
