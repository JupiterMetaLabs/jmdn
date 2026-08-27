package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"syscall"
	"time"

	"gossipnode/config/GRO"
	"gossipnode/shutdown"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/builder"
	thebeconfig "github.com/JupiterMetaLabs/ThebeDB/pkg/config"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/profile"
	thebeSql "github.com/JupiterMetaLabs/ThebeDB/pkg/sql"
	orchestratorGlobal "github.com/JupiterMetaLabs/goroutine-orchestrator/manager/global"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/ethereum/go-ethereum/common"

	MessagePassing "gossipnode/AVC/BuddyNodes/MessagePassing"
	MsgPassingService "gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	"gossipnode/Block"
	"gossipnode/CA/tlsca"
	cli "gossipnode/CLI"
	"gossipnode/DB_OPs"
	NodeInfo "gossipnode/DB_OPs/Nodeinfo"
	"gossipnode/DB_OPs/backend"
	"gossipnode/DB_OPs/cassata"
	"gossipnode/DB_OPs/contractDB"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/DB_OPs/thebeprofile"
	"gossipnode/DB_OPs/txindex"
	"gossipnode/DID"
	"gossipnode/Pubsub"
	"gossipnode/Security"
	"gossipnode/Sequencer"
	"gossipnode/SmartContract"
	"gossipnode/SmartContract/evmexec"
	"gossipnode/blockgossip"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/config/version"
	"gossipnode/consensushash"
	"gossipnode/explorer"
	"gossipnode/gETH"
	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/rpc"
	"gossipnode/helper"
	"gossipnode/internal/syncmonitor"
	"gossipnode/messaging"
	"gossipnode/messaging/directMSG"
	"gossipnode/metrics"
	"gossipnode/node"
	"gossipnode/profiler"
	"gossipnode/seednode"
	"gossipnode/thebesync"

	ion "github.com/JupiterMetaLabs/ion"
	"github.com/redis/go-redis/v9"

	"github.com/libp2p/go-libp2p/core/host"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"
)

var (
	MainAM interfaces.AppGoroutineManagerInterface
	MainLM interfaces.LocalGoroutineManagerInterface
)

var groTrackingEnabled bool

func shouldEnableGROTracking(grotrack bool, metricsEnabled bool) bool {
	if !grotrack {
		return false
	}
	if !metricsEnabled {
		return false
	}
	return true
}

func goMaybeTracked(
	localMgr interfaces.LocalGoroutineManagerInterface,
	appName string,
	localName string,
	threadName string,
	fn func(ctx context.Context) error,
	opts ...interfaces.GoroutineOption,
) error {
	if localMgr == nil {
		return fmt.Errorf("local manager is nil (thread=%s)", threadName)
	}
	if groTrackingEnabled {
		return metrics.GoTracked(localMgr, appName, localName, threadName, fn, opts...)
	}
	return localMgr.Go(threadName, fn, opts...)
}

// Global variables for easier access
var (
	globalPubSub *Pubsub.StructGossipPubSub
)

// Global connection pools
var (
	mainDBPool     *config.ConnectionPool // Main database connection pool
	accountsDBPool *config.ConnectionPool // Accounts/DID database connection pool
	cas            *cassata.Cassata
)

func initGlobalGRO() {
	// This is the creation an setting of the global GRO manager
	GRO.InitGlobal()

	// Ensure global manager is initialized before we mutate metadata.
	if _, err := GRO.GlobalGRO.Init(); err != nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Failed to initialize global GRO manager", err)
		}
		os.Exit(1)
	}

	// Set the global shutdown timeout to 10 seconds.
	if _, err := GRO.GlobalGRO.UpdateMetadata(
		orchestratorGlobal.SET_SHUTDOWN_TIMEOUT,
		10*time.Second,
	); err != nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Failed to set GRO shutdown timeout metadata", err)
		}
		os.Exit(1)
	}
}

func initAppandLocalGRO() {

	var err error
	// Also pull up new app manager - main for the main package
	err = GRO.EagerLoading()
	if err != nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Failed to eager load GRO", err)
		}
		os.Exit(1)
	}

	MainAM = GRO.GetApp(GRO.MainAM)

	MainLM, err = MainAM.NewLocalManager(GRO.MainLM)
	if err != nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Failed to create local manager", err)
		}
		os.Exit(1)
	}
}

func StartFacadeServer(bindAddr string, port int, debugBindAddr string, debugPort int, chainID int, smartRPC int, mon *syncmonitor.Monitor) {
	if MainLM == nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "MainLM not initialized. Call initAppandLocalGRO() first", nil)
		}
		os.Exit(1)
	}

	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.FacadeThread, func(ctx context.Context) error {
		if logger := mainLogger(); logger != nil {
			logger.Info(ctx, "Starting facade server")
		}

		handler := rpc.NewHandlers(Service.NewService(chainID, smartRPC))
		httpServer := rpc.NewHTTPServer(handler)
		if cas != nil {
			httpServer = httpServer.WithCassata(cas)
		}
		if mon != nil {
			httpServer.WithSyncMonitor(mon)
		}

		if debugPort > 0 {
			debugAddr := fmt.Sprintf("%s:%d", debugBindAddr, debugPort)
			go func() {
				if err := httpServer.ServeDebugWithContext(ctx, debugAddr); err != nil {
					if logger := mainLogger(); logger != nil {
						logger.Error(ctx, "Facade debug server stopped", err, ion.String("addr", debugAddr))
					}
				}
			}()
		}

		addr := fmt.Sprintf("%s:%d", bindAddr, port)
		if err := httpServer.ServeWithContext(ctx, addr); err != nil {
			if logger := mainLogger(); logger != nil {
				logger.Error(ctx, "Facade server stopped", err, ion.String("addr", addr))
			}
			return fmt.Errorf("facade server failed: %w", err)
		}
		return nil
	}); err != nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.FacadeThread))
		}
	}
}

func StartWSServer(bindAddr string, port int, chainID int, smartRPC int) {
	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.WSServerThread, func(ctx context.Context) error {
		if logger := mainLogger(); logger != nil {
			logger.Info(ctx, "Starting WSServer")
		}
		// Get the Http Server
		HTTPServer := rpc.NewHandlers(Service.NewService(chainID, smartRPC))

		WSServer := rpc.NewWSServer(HTTPServer, Service.NewService(chainID, smartRPC))
		if err := WSServer.ServeWithContext(ctx, fmt.Sprintf("%s:%d", bindAddr, port)); err != nil {
			if logger := mainLogger(); logger != nil {
				logger.Error(ctx, "Failed to start WSServer", err)
			}
			return fmt.Errorf("WSServer failed: %w", err)
		}
		return nil
	}); err != nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.WSServerThread))
		}
	}
}

// GetMainDBPool returns the global main database connection pool
func GetMainDBPool() *config.ConnectionPool {
	if mainDBPool == nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Main DB pool not initialized. Call initMainDBPool first", nil)
		}
		os.Exit(1)
	}
	return mainDBPool
}

// GetAccountsDBPool returns the global accounts database connection pool
func GetAccountsDBPool() *config.ConnectionPool {
	if accountsDBPool == nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Accounts DB pool not initialized. Call initAccountsDBPool first", nil)
		}
		os.Exit(1)
	}
	return accountsDBPool
}

// GetGlobalPubSub returns the global PubSub instance
func GetGlobalPubSub() *Pubsub.StructGossipPubSub {
	if globalPubSub == nil {
		if logger := mainLogger(); logger != nil {
			logger.Warn(context.Background(), "Global PubSub not initialized - PubSub features may be limited")
		}
	}
	return globalPubSub
}

// formatTimestamp formats a time.Time as "DD-MM-YYYY HH:MM:SS" (readable format)
// Converts UTC time to local time before formatting
func formatTimestamp(t time.Time) string {
	// Convert UTC to local time
	localTime := t.Local()
	return localTime.Format("02-01-2006 15:04:05")
}

func maskDSN(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid>"
	}
	if u.User != nil {
		username := u.User.Username()
		if username != "" {
			u.User = url.UserPassword(username, "***")
		} else {
			u.User = url.UserPassword("***", "***")
		}
	}
	return u.String()
}

func maskRedisURL(raw string) string {
	return maskDSN(raw)
}

// runCommand executes a CLI command via gRPC to the running service
func runCommand(command string, args []string, grpcPort int) {
	// Special handling for version command - we want it to work even if node is offline
	if command == "version" {
		fmt.Println("Local Binary Version:")
		fmt.Println(version.String())
		fmt.Println("----------------------------------------")

		client, err := cli.NewClient(fmt.Sprintf("localhost:%d", grpcPort))
		if err == nil {
			defer client.Close()
			v, err := client.GetNodeVersion()
			if err == nil {
				fmt.Println("Remote Node Version (Running):")
				fmt.Printf("Tag: %s, Branch: %s, Commit: %s, Built: %s, Go: %s\n",
					v.GitTag, v.GitBranch, v.GitCommit, v.BuildTime, v.GoVersion)
			} else {
				// Connected but call failed
				fmt.Printf("Could not fetch remote version: %v\n", err)
			}
		} else {
			// Could not connect
			fmt.Println("Could not connect to running node (Offline?).")
		}
		os.Exit(0)
	}

	client, err := cli.NewClient(fmt.Sprintf("localhost:%d", grpcPort))
	if err != nil {
		fmt.Printf("Error connecting to gRPC server: %v\n", err)
		fmt.Println("Make sure the service is running.")
		os.Exit(1)
	}
	defer client.Close()

	switch command {

	case "help":
		fmt.Println("\nAvailable CLI Commands:")
		fmt.Println("  listpeers, list     - List all managed peers")
		fmt.Println("  addrs                - Show node addresses")
		fmt.Println("  stats                - Show messaging statistics")
		fmt.Println("  dbstate              - Show database state")
		fmt.Println("  addpeer <addr>       - Add a peer")
		fmt.Println("  removepeer <id>      - Remove a peer")
		fmt.Println("  cleanpeers           - Clean offline peers")
		fmt.Println("  sendmsg <tgt> <msg>  - Send message")
		fmt.Println("  broadcast <msg>      - Broadcast message")
		fmt.Println("  getdid <did>         - Get DID document")
		fmt.Println("  propagatedid <did> <public_key> [balance] - Propagate DID to network")
		fmt.Println("  fastsync <peer>                   - Fast sync with peer (V2 Engine)")
		fmt.Println("  catchup <peer> [from_block]       - Catch up to chain tip; from_block defaults to auto-detect (localTip+1)")
		fmt.Println("  rebuildindex                      - Wipe and rebuild tx-address index from genesis (fixes all gaps)")
		fmt.Println("  rebuildrange <from> <to>          - Re-index a specific block range (targeted gap repair)")
		fmt.Println("  txindexstatus                     - Show tx-address index sync status (ready/syncing, last indexed block)")
		fmt.Println("  accountsync <peer>                - Sync missing accounts only (skip block sync)")
		fmt.Println("\nUsage: ./jmdn -cmd <command> [args...]")
		fmt.Println("\nNote: Some interactive commands (mempoolStats, seednodeStats, etc.)")
		fmt.Println("are only available in interactive mode.")

	case "listpeers", "list":
		peers, err := client.ListPeers()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nPeers (%d):\n", len(peers.Peers))
		for _, peer := range peers.Peers {
			status := "OFFLINE"
			if peer.IsAlive {
				status = "ONLINE"
			}
			fmt.Printf("  %s - %s [%s] Last: %s\n",
				peer.Id, peer.Multiaddr, status, peer.LastSeen)
		}

	case "addrs":
		addrs, err := client.ReturnAddrs()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nNode Addresses:\n")
		for _, addr := range addrs.Peers {
			fmt.Printf("  %s\n", addr)
		}

	case "stats":
		stats, err := client.GetMessageStats()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nMessaging Statistics:\n")
		fmt.Printf("  Sent:     %d\n", stats.MessagesSent)
		fmt.Printf("  Received: %d\n", stats.MessagesReceived)
		fmt.Printf("  Failed:   %d\n", stats.MessagesFailed)

	case "dbstate":
		state, err := client.GetDatabaseState()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nDatabase State:\n")
		fmt.Printf("  Main DB TxID:     %d\n", state.MainDb.TxId)
		fmt.Printf("  Accounts DB TxID: %d\n", state.AccountsDb.TxId)

	case "addpeer":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd addpeer <peer_multiaddr>")
			os.Exit(1)
		}
		resp, err := client.AddPeer(args[0])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	case "removepeer":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd removepeer <peer_id>")
			os.Exit(1)
		}
		resp, err := client.RemovePeer(args[0])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	case "cleanpeers":
		resp, err := client.CleanPeers()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Cleaned %d peers\n", resp.CleanedCount)

	case "sendmsg":
		if len(args) < 2 {
			fmt.Println("Usage: jmdn -cmd sendmsg <target> <message>")
			os.Exit(1)
		}
		resp, err := client.SendMessage(args[0], strings.Join(args[1:], " "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	case "broadcast":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd broadcast <message>")
			os.Exit(1)
		}
		resp, err := client.BroadcastMessage(strings.Join(args, " "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	case "getdid":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd getdid <did>")
			os.Exit(1)
		}
		doc, err := client.GetDID(args[0])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nDID Document:\n")
		fmt.Printf("  DID:       %s\n", doc.Did)
		fmt.Printf("  PublicKey: %s\n", doc.PublicKey)
		fmt.Printf("  Balance:   %s\n", doc.Balance)

		// Format CreatedAt timestamp as DD-MM-YYYY HH:MM:SS
		if doc.CreatedAt != nil {
			createdAt := doc.CreatedAt.AsTime()
			fmt.Printf("  CreatedAt: %s\n", formatTimestamp(createdAt))
		}

		// Format UpdatedAt timestamp as DD-MM-YYYY HH:MM:SS
		if doc.UpdatedAt != nil {
			updatedAt := doc.UpdatedAt.AsTime()
			fmt.Printf("  UpdatedAt: %s\n", formatTimestamp(updatedAt))
		}

	case "propagatedid":
		if len(args) < 2 {
			fmt.Println("Usage: jmdn -cmd propagatedid <did> <public_key> [balance]")
			os.Exit(1)
		}
		balance := "0"
		if len(args) >= 3 {
			balance = args[2]
		}
		resp, err := client.PropagateDID(args[0], args[1], balance)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if resp.Success {
			fmt.Printf("Success: %s\n", resp.Message)
		} else {
			fmt.Printf("Error: %s\n", resp.Message)
			os.Exit(1)
		}

	case "fastsync", "fastsyncv2", "firstsync":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd fastsync <peer_multiaddr>")
			os.Exit(1)
		}
		fmt.Println("Starting FastSync (V2 Engine)...")
		stats, err := client.FastSyncV2(args[0])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if stats == nil {
			fmt.Println("FastSync returned no stats. The target peer may be unreachable.")
			os.Exit(1)
		}
		if stats.Error != "" {
			fmt.Printf("FastSync failed: %s\n", stats.Error)
			os.Exit(1)
		}
		fmt.Printf("Sync completed in %ds\n", stats.TimeTaken)
		if stats.MainState == nil {
			fmt.Println("  Main DB TxID: unavailable")
		} else {
			fmt.Printf("  Main DB TxID: %d\n", stats.MainState.TxId)
		}
		if stats.AccountsState == nil {
			fmt.Println("  Accounts DB TxID: unavailable")
		} else {
			fmt.Printf("  Accounts DB TxID: %d\n", stats.AccountsState.TxId)
		}

	case "catchup":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd catchup <peer_multiaddr> [from_block]")
			fmt.Println("  from_block defaults to 0 (auto-detect from local DB tip)")
			os.Exit(1)
		}
		var fromBlock uint64
		if len(args) >= 2 {
			var err error
			fromBlock, err = strconv.ParseUint(args[1], 10, 64)
			if err != nil {
				fmt.Printf("Invalid from_block %q: %v\n", args[1], err)
				os.Exit(1)
			}
		}
		fmt.Printf("Starting CatchUpSync (from_block=%d)...\n", fromBlock)
		stats, err := client.CatchUpSync(args[0], fromBlock)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if stats != nil && stats.Error != "" {
			fmt.Printf("CatchUpSync failed: %s\n", stats.Error)
			os.Exit(1)
		}
		if stats != nil {
			fmt.Printf("CatchUpSync completed in %ds\n", stats.TimeTaken)
		}

	case "rebuildindex":
		fmt.Println("Rebuilding tx-address index from genesis (this may take a while)...")
		resp, err := client.RebuildTxIndex()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if resp.Error != "" {
			fmt.Printf("RebuildIndex failed: %s\n", resp.Error)
			os.Exit(1)
		}
		fmt.Printf("RebuildIndex complete in %v\n", time.Duration(resp.TimeTakenMs)*time.Millisecond)

	case "rebuildrange":
		if len(args) != 2 {
			fmt.Println("Usage: jmdn -cmd rebuildrange <from_block> <to_block>")
			os.Exit(1)
		}
		from, err := strconv.ParseUint(args[0], 10, 64)
		if err != nil {
			fmt.Printf("Invalid from_block %q: %v\n", args[0], err)
			os.Exit(1)
		}
		to, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			fmt.Printf("Invalid to_block %q: %v\n", args[1], err)
			os.Exit(1)
		}
		fmt.Printf("Re-indexing blocks [%d..%d]...\n", from, to)
		resp, err := client.RebuildTxIndexRange(from, to)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if resp.Error != "" {
			fmt.Printf("RebuildRange failed: %s\n", resp.Error)
			os.Exit(1)
		}
		fmt.Printf("RebuildRange [%d..%d] complete in %v\n", from, to, time.Duration(resp.TimeTakenMs)*time.Millisecond)

	case "txindexstatus":
		resp, err := client.GetTxIndexStatus()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if resp.Error != "" {
			fmt.Printf("txindex status: %s\n", resp.Error)
			os.Exit(1)
		}
		state := "SYNCING (catchup in progress)"
		if resp.Ready {
			state = "READY"
		}
		fmt.Printf("txindex status: %s — last indexed block: %d\n", state, resp.LastIndexedBlock)

	case "accountsync":
		if len(args) < 1 {
			fmt.Println("Usage: jmdn -cmd accountsync <peer_multiaddr>")
			os.Exit(1)
		}
		fmt.Println("Starting AccountSync (accounts only, no block sync)...")
		stats, err := client.AccountSync(args[0])
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if stats.Error != "" {
			fmt.Printf("AccountSync failed: %s\n", stats.Error)
			os.Exit(1)
		}
		fmt.Printf("AccountSync completed in %ds\n", stats.TimeTaken)
		if stats.AccountsState != nil {
			fmt.Printf("  Accounts DB TxID: %d\n", stats.AccountsState.TxId)
		}

	case "ygg":
		if len(args) < 2 {
			fmt.Println("Usage: jmdn -cmd ygg <target> <message>")
			os.Exit(1)
		}
		resp, err := client.SendYggdrasilMessage(args[0], strings.Join(args[1:], " "))
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Result: %s\n", resp.Message)

	default:
		fmt.Printf("Unknown command: %s\n", command)
		fmt.Println("\nAvailable commands:")
		fmt.Println("  help                 - Show this help message")
		fmt.Println("  listpeers, list      - List all managed peers")
		fmt.Println("  addrs                - Show node addresses")
		fmt.Println("  stats                - Show messaging statistics")
		fmt.Println("  dbstate              - Show database state")
		fmt.Println("  addpeer <addr>       - Add a peer")
		fmt.Println("  removepeer <id>     - Remove a peer")
		fmt.Println("  cleanpeers          - Clean offline peers")
		fmt.Println("  sendmsg <tgt> <msg>  - Send message via libp2p")
		fmt.Println("  ygg <tgt> <msg>      - Send message via Yggdrasil")
		fmt.Println("  broadcast <msg>      - Broadcast message")
		fmt.Println("  getdid <did>         - Get DID document")
		fmt.Println("  fastsync <peer>                   - Fast sync with peer (V2 Engine)")
		fmt.Println("  catchup <peer> [from_block]       - Catch up to chain tip; from_block defaults to auto-detect (localTip+1)")
		fmt.Println("  rebuildindex                      - Wipe and rebuild tx-address index from genesis (fixes all gaps)")
		fmt.Println("  rebuildrange <from> <to>          - Re-index a specific block range (targeted gap repair)")
		fmt.Println("  txindexstatus                     - Show tx-address index sync status (ready/syncing, last indexed block)")
		fmt.Println("  accountsync <peer>                - Sync missing accounts only (skip block sync)")
		os.Exit(1)
	}
}

func StartAPIServer(ctx context.Context, address string) error {
	// Create Explorer API server
	server, err := explorer.NewExplorerServer()
	if err != nil {
		return fmt.Errorf("failed to create Explorer API server: %w", err)
	}

	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.BlockPollerThread, func(ctx context.Context) error {
		explorer.StartBlockPoller(ctx, server, 7*time.Second)
		return nil
	}); err != nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.BlockPollerThread))
		}
	}

	if logger := mainLogger(); logger != nil {
		logger.Info(context.Background(), "Starting Explorer API server", ion.String("address", address))
	}
	return server.StartWithContext(ctx, address)
}

// Update this function:
func startDIDServer(ctx context.Context, h host.Host, address string) error {
	mainLogger().Info(context.Background(), "DID propagation initialized successfully")
	return DID.StartDIDServerWithContext(ctx, h, address, nil)
}

// initYggdrasilMessaging initializes the Yggdrasil messaging system
func initYggdrasilMessaging(ctx context.Context) {
	directMSG.StartYggdrasilListener(ctx)
	// Assign yggdraisl address to the config.Yggdrasil_Address

	fmt.Println(config.ColorGreen+"Yggdrasil messaging service started on port:"+config.ColorReset, directMSG.YggdrasilPort)
}

// Initialize main database connection pool
func initMainDBPool(logger_ctx context.Context, enableLoki bool) error {
	poolingConfig := &config.PoolingConfig{
		DBName: config.DBName,
	}

	// Initialize the global pool. The factory is supplied process-wide via
	// config.SetGlobalHandleFactory once ThebeDB is constructed (initThebeBackend).
	config.InitGlobalPoolWithLoki(poolingConfig, nil)

	mainDBPool = config.GetGlobalPool(logger_ctx)

	// Also initialize the DB_OPs main pool
	if logger := mainLogger(); logger != nil {
		logger.Debug(context.Background(), "Initializing DB_OPs main pool...")
	}

	if logger := mainLogger(); logger != nil {
		logger.Info(context.Background(), "Main database connection pool initialized", ion.String("database", config.DBName))
	}
	return nil
}

// Initialize accounts database connection pool
func initAccountsDBPool() error {
	if logger := mainLogger(); logger != nil {
		logger.Info(context.Background(), "Accounts database connection pool initialized", ion.String("database", config.AccountsDBName))
	}
	return nil
}

// FastsyncV2 retired: sync serving is handled by the ThebeSync (FastSync v4)
// handlers registered in node.go, and catch-up by thebesync.CatchUp (wired into
// the sync monitor below). No engine object is initialized.

// initPubSub initializes the PubSub system for the node
func initPubSub(n *config.Node) (*Pubsub.StructGossipPubSub, error) {
	fmt.Println("Initializing PubSub system...")

	// Create a protocol ID for PubSub (using the consensus channel name as protocol)
	pubSubProtocol := config.BuddyNodesMessageProtocol

	// Initialize the GossipPubSub instance
	gossipPubSub, err := Pubsub.NewGossipPubSub(n.Host, pubSubProtocol)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GossipPubSub: %w", err)
	}

	fmt.Printf("✅ PubSub system initialized successfully for host: %s\n", n.Host.ID())
	fmt.Printf("📡 PubSub protocol: %s\n", pubSubProtocol)

	return gossipPubSub, nil
}

func main() {
	logger_ctx, logger_cancel := context.WithCancel(context.Background())
	defer logger_cancel()

	// Command-line flags for node configuration
	seedNodeURL := flag.String("seednode", "", "Seed node gRPC URL for peer registration (e.g., localhost:9090)")
	peerAlias := flag.String("alias", "", "Peer alias for registration with seed node")
	heartbeatInterval := flag.Int("heartbeat", 120, "Heartbeat interval in seconds (default: 300)")
	metricsPort := flag.String("metrics", "", "Port for Prometheus metrics (empty disables metrics server)")
	profilerPort := flag.String("profiler", "", "Port for Go profiler (pprof) (empty disables profiler server)")
	grotrack := flag.Bool("grotrack", false, "Track GRO goroutines in Prometheus/Grafana (requires -metrics)")
	enableYggdrasil := flag.Bool("ygg", true, "Enable Yggdrasil direct messaging (default: true)")
	apiPort := flag.Int("api", 0, "Run Explorer API on specified port (0 = disabled)")
	blockgen := flag.Int("blockgen", 0, "Run Block creator API on specified port (0 = disabled)")
	blockgRPC := flag.Int("blockgrpc", 0, "Run Block gRPC server on specified port (0 = disabled)")
	mempoolgRPC := flag.String("mempool", "localhost:15051", "Mempool gRPC server address")
	cliGRPC := flag.Int("cli", 15053, "CLI gRPC server address")
	DIDPort := flag.Int("did", 15052, "DID gRPC server port")
	gETHgRPC := flag.Int("geth", 15054, "gETH gRPC server address")
	gETHFacade := flag.Int("facade", 8545, "gETH Facade server address")
	gETHWSServer := flag.Int("ws", 8546, "gETH WSServer address")
	smartRPC := flag.Int("smart", 15056, "Smart Contract gRPC server address")
	chainID := flag.Int("chainID", 7000700, "Chain ID for the blockchain network")
	explorerAPIKey := flag.String("explorer-api-key", "", "Explorer API key")
	jwtSecret := flag.String("jwt-secret", "", "JWT secret")
	command := flag.String("cmd", "", "Execute a CLI command (e.g., listpeers, addrs, stats, dbstate)")
	versionFlag := flag.Bool("version", false, "Print version information and exit")

	// Parse flags
	flag.Parse()

	// Exit immediately if version flag is set, before ANY initialization
	// This prevents any side effects from package imports or init() functions
	if *versionFlag {
		fmt.Println(version.String())
		return
	}

	// ----------------------------------------------------
	// Load unified configuration (defaults + yaml + env)
	// ----------------------------------------------------
	cfg, cfgErr := settings.Load()
	if cfgErr != nil {
		fmt.Printf("Failed to load configuration: %v\n", cfgErr)
		os.Exit(1)
	}

	// Apply CLI flag overrides (only flags explicitly passed on command line)
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "seednode":
			cfg.Network.SeedNode = *seedNodeURL
		case "alias":
			cfg.Node.Alias = *peerAlias
		case "heartbeat":
			cfg.Network.HeartbeatInterval = *heartbeatInterval
		case "metrics":
			cfg.Ports.Metrics, _ = strconv.Atoi(*metricsPort)
		case "profiler":
			cfg.Ports.Profiler, _ = strconv.Atoi(*profilerPort)
		case "grotrack":
			cfg.Features.GROTrack = *grotrack
		case "ygg":
			cfg.Network.Yggdrasil = *enableYggdrasil
		case "api":
			cfg.Ports.API = *apiPort
		case "blockgen":
			cfg.Ports.BlockGen = *blockgen
		case "blockgrpc":
			cfg.Ports.BlockGRPC = *blockgRPC
		case "mempool":
			cfg.Network.Mempool = *mempoolgRPC
		case "cli":
			cfg.Ports.CLI = *cliGRPC
		case "did":
			cfg.Ports.DID = *DIDPort
		case "geth":
			cfg.Ports.Geth = *gETHgRPC
		case "facade":
			cfg.Ports.Facade = *gETHFacade
		case "ws":
			cfg.Ports.WS = *gETHWSServer
		case "smart":
			cfg.Ports.Smart = *smartRPC
		case "chainID":
			cfg.Network.ChainID = *chainID
		case "explorer-api-key":
			cfg.Security.ExplorerAPIKey = *explorerAPIKey
		case "jwt-secret":
			cfg.Security.JWTSecret = *jwtSecret
		}
	})

	// RE-RESOLVE TOKENS: CLI flags might have updated secrets (ExplorerAPIKey, JWTSecret).
	// We must refresh the token cache so GetResolvedToken() returns the correct values.
	cfg.Security.ResolveTokens()

	// SEC-03: security posture check. Warn loudly for every gatekeeper HTTP
	// service left unauthenticated on a public (non-loopback) bind; when
	// security.strict_posture is set, REFUSE to boot (fail closed).
	for _, v := range cfg.InsecurePublicServices() {
		// NEW-5: surface v.Reason so the remedy matches the ACTUAL violation.
		// "auth_type=none" → set token/mtls; "public RPC with no rate limit" → set
		// a rate limit (the RPC is intentionally unauthenticated). The old hardcoded
		// message told operators to token-gate the JSON-RPC, which is wrong for the
		// rate-limit case.
		log.Warn().
			Str("service", v.Service).
			Str("bind", v.Bind).
			Str("reason", v.Reason).
			Msg("SEC-03: insecure gatekeeper service on a public bind — remediate per 'reason' (auth_type=none → set auth_type token/mtls; no rate limit → set a rate limit), restrict the bind to loopback, or enable security.strict_posture to fail closed")
	}
	if err := cfg.ValidateSecurityPosture(); err != nil {
		fmt.Printf("Refusing to start: %v\n", err)
		os.Exit(1)
	}

	log.Info().
		Bool("enabled", cfg.Thebe.Enabled).
		Str("kv_path", cfg.Thebe.KVPath).
		Str("sql_dsn", maskDSN(cfg.Thebe.SQLDSN)).
		Str("redis_url", maskRedisURL(cfg.Thebe.RedisURL)).
		Str("stream_name", cfg.Thebe.StreamName).
		Int64("max_len", cfg.Thebe.MaxLen).
		Str("group_name", cfg.Thebe.GroupName).
		Msg("Resolved Thebe config")

	// Chain ID global initialization — must happen before any Security validation.
	// Setting this globally here (rather than only inside Block/Server.go, gated behind
	// BlockGen > 0) keeps expectedChainID set on non-sequencer nodes. All nodes need it because
	// Security.allChecksWithConn validates chain ID on both direct tx submission
	// (Block/Server.go:188 → AllChecks) and broadcast vote triggers
	// (node/node.go:199 → messaging.HandleBroadcastStream → Vote.SubmitVote → CheckZKBlockValidation).
	if cfg.Network.ChainID <= 0 {
		fmt.Printf("FATAL: invalid chain_id %d in config — must be a positive integer\n", cfg.Network.ChainID)
		os.Exit(1)
	}
	Security.SetExpectedChainIDBig(big.NewInt(int64(cfg.Network.ChainID)))
	fmt.Printf("Global expected chain ID configured: %d\n", cfg.Network.ChainID)

	// Initialize Global Go Routine Orchestrator first
	initGlobalGRO()
	initAppandLocalGRO()

	// Initialize messaging cleanup routines
	messaging.StartBlockPropagationCleanup()
	messaging.StartBroadcastCleanup()

	var nodeManager *node.NodeManager
	if err := tlsca.EnsureTLSAssets(".immudb_state"); err != nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Failed to ensure TLS assets", err)
		}
		os.Exit(1)
	}
	// fmt.Println("ImmuDB TLS assets generated.")

	// Handle command execution mode - if -cmd is provided, execute command via gRPC and exit
	if *command != "" {
		runCommand(*command, flag.Args(), cfg.Ports.CLI)
		return
	}

	groTrackingEnabled = shouldEnableGROTracking(cfg.Features.GROTrack, cfg.Ports.Metrics > 0)

	// Start metrics server only when a metrics port is provided.
	if cfg.Ports.Metrics > 0 {
		metricsAddr := fmt.Sprintf("%s:%d", cfg.Binds.Metrics, cfg.Ports.Metrics)
		metrics.StartMetricsServer(metricsAddr)
		fmt.Printf(
			config.ColorGreen+"\nMetrics available at "+config.ColorReset+"http://%s:%d/metrics\n",
			cfg.Binds.Metrics,
			cfg.Ports.Metrics,
		)
	} else if cfg.Features.GROTrack {
		if logger := mainLogger(); logger != nil {
			logger.Warn(context.Background(), "grotrack enabled but metrics port is not set; GRO tracking disabled")
		}
	}

	// Start profiler server only when a profiler port is explicitly set (> 0).
	// Access profiles at http://localhost:<port>/debug/pprof/
	// Start profiler server only when a profiler port is provided.
	// Access profiles at http://localhost:<port>/debug/pprof/
	var profilerServer *http.Server
	if cfg.Ports.Profiler > 0 {
		// Fallback to the default pprof port if none specified.
		profilerPortStr := fmt.Sprintf("%d", cfg.Ports.Profiler)
		profilerServer = profiler.StartProfiler(cfg.Binds.Profiler, profilerPortStr)
		fmt.Printf(
			config.ColorGreen+"\nProfiler available at "+config.ColorReset+"http://%s:%s/debug/pprof/\n",
			cfg.Binds.Profiler,
			profilerPortStr,
		)
	}

	// Log version on startup
	if logger := mainLogger(); logger != nil {
		logger.Info(context.Background(), "Starting JMDN node", ion.String("version", version.String()))
	}

	// Create a cancellable context for clean shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// shutdownSequenceBudget bounds steps 1-4 below as a whole. Each step is
	// already individually bounded (profiler: 5s; shutdown.Shutdown()'s GRO
	// window: ~10s + ~0.4s of flush sleeps), but no single bound otherwise caps
	// the SEQUENCE — a stall anywhere in it could still run past Docker's
	// `stop_grace_period: 30s` (docker-compose.yml) and get SIGKILLed with
	// no log line explaining why. Kept comfortably under that 30s so this
	// fires first, logs why, and exits cleanly instead of being killed blind.
	const shutdownSequenceBudget = 25 * time.Second

	if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.ShutdownThread, func(ctx context.Context) error {
		<-sigCh

		fmt.Println("\nShutdown signal received, closing connections...")

		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)

			// 1. Cancel the main context to stop context-aware components (e.g., Yggdrasil, API)
			cancel()

			// 2. Shutdown profiler concurrently with other cleanups (with timeout)
			if profilerServer != nil {
				if logger := mainLogger(); logger != nil {
					logger.Info(ctx, "Shutting down profiler server...")
				}
				// Give it 5 seconds to finish active profiles/requests
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutdownCancel()
				if err := profilerServer.Shutdown(shutdownCtx); err != nil {
					if logger := mainLogger(); logger != nil {
						logger.Error(ctx, "Profiler server forced to shutdown", err)
					}
				} else {
					if logger := mainLogger(); logger != nil {
						logger.Info(ctx, "Profiler server stopped gracefully")
					}
				}
			}

			// 3. Stop the tx-address index: refuse new async work, cancel any
			// in-flight catchup/rebuild, and close both SQLite pools. Must run
			// before the process exits — nothing else does this today, and an
			// unclosed sql.DB leaks its connections/WAL file handles.
			log.Info().Msg("Shutting down transaction address index...")
			if err := txindex.Shutdown(); err != nil {
				log.Error().Err(err).Msg("txindex shutdown reported an error")
			} else {
				log.Info().Msg("Transaction address index stopped")
			}

			// 4. Delegate final shutdown to the centralized handler
			if shutdown.Shutdown() {
				logger_cancel()
				defer shutdown.OS_EXIT(0)
			}
		}()

		select {
		case <-shutdownDone:
			// Completed within budget. If step 4 succeeded, shutdown.OS_EXIT(0)
			// already terminated the process before this select could observe
			// the close — this case is only reachable if shutdown.Shutdown()
			// returned false (no exit call was made).
		case <-time.After(shutdownSequenceBudget):
			log.Error().Msg("shutdown sequence exceeded its budget — forcing exit so Docker's stop_grace_period doesn't SIGKILL blind")
			shutdown.OS_EXIT(1)
		}
		return nil
	}); err != nil {
		if logger := mainLogger(); logger != nil {
			logger.Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.ShutdownThread))
		}
	}

	// Initialize database connection pools FIRST
	fmt.Println("Initializing main database pool...")
	if err := initMainDBPool(logger_ctx, false); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize main database pool")
	}
	fmt.Println("Main database pool initialized successfully")

	// Initialise the SQLite tx-by-address index. Init() only opens the DB file
	// and starts the background worker — it returns immediately. The (possibly
	// long, e.g. full genesis migration on first deploy) gap catchup runs in a
	// goroutine so it never delays facade/RPC/consensus/gossip startup below.
	// Until txindex.IsReady() is true, eth_getTransactionsByAddress and
	// getAddressTransactions return a "still syncing" / 503 error rather than
	// an ImmuDB-scan fallback, which no longer exists (see PR history).
	txIndexPath := cfg.Database.TxIndexPath
	if txIndexPath == "" {
		txIndexPath = "./DB/txindex.db" // matches config/settings/defaults.go default
	}
	if err := txindex.Init(logger_ctx, txIndexPath); err != nil {
		// Only Open() (disk/permissions) failures land here — catchup failures
		// are logged asynchronously by the background goroutine.
		log.Warn().Err(err).Msg("txindex init failed — address-by-tx lookups will error until this is resolved (see CLI `rebuildindex`)")
	} else {
		fmt.Println("Transaction address index starting (background catchup in progress)")
	}

	if err := initAccountsDBPool(); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize accounts database pool")
	}

	// Initialize ThebeDB + JMDN profile only when feature-flagged.
	if cfg.Thebe.Enabled {
		fmt.Fprintf(os.Stderr, "thebedb: init — kv_path=%s dsn=%s\n", cfg.Thebe.KVPath, maskDSN(cfg.Thebe.SQLDSN))

		reg := profile.NewRegistry()
		reg.Register(thebeprofile.NewJMDNProfile())

		kvStore, err := kv.NewStore(kv.Config{Backend: kv.BackendBadger, Path: cfg.Thebe.KVPath})
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL thebedb: kv store init failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "thebedb: kv store OK")

		sqlEngine, err := thebeSql.NewSQLEngine(cfg.Thebe.SQLDSN)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL thebedb: sql engine init failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "thebedb: sql engine OK")

		db, err := thebedb.New(kvStore, sqlEngine, thebedb.WithProfileRegistry(reg))
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL thebedb: db init failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "thebedb: db init OK")
		defer db.Close()

		// Wire CDC if enabled.
		// IMPORTANT: pass cfg.Thebe.SQLDSN (direct Postgres DSN), never PgBouncer —
		// logical replication is session-level and breaks over transaction pooling.
		if cfg.Thebe.CDC.Enabled {
			cdcCfg := thebeconfig.CDC{
				Enabled:     true,
				SlotName:    cfg.Thebe.CDC.SlotName,
				Publication: cfg.Thebe.CDC.Publication,
				LogPath:     cfg.Thebe.CDC.LogPath,
				DLQPath:     cfg.Thebe.CDC.DLQPath,
				MaxLagBytes: cfg.Thebe.CDC.MaxLagBytes,
			}
			if err := db.StartCDC(cdcCfg, cfg.Thebe.SQLDSN); err != nil {
				fmt.Fprintf(os.Stderr, "FATAL thebedb: cdc start failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "thebedb: CDC pipeline running")
		}

		// Keep cassata for backward-compat callers (SmartContract, gETH routes).
		cas = cassata.New(db, zap.NewNop())

		// EVM contract execution (audit EVM-01 wiring). Registered ONLY when
		// cfg.Contracts.Enabled; default-off leaves execbridge on its no-op so the
		// apply path is byte-identical (non-breaking). The executor reads balances
		// from the local committed ledger (deterministic, EVM-A16) and returns value
		// deltas for the centralized fold (config.FoldContractExecution).
		if cfg.Contracts.Enabled {
			// Derive hasCode from the SAME persistent repo passed to Register — its
			// GetCode reads cas.KV() (the store deploys commit to and eth_getCode
			// reads). The old contractDB.HasCode read a process-wide sharedKVStore
			// singleton that is never set (SetSharedKVStore has zero callers), so it
			// always returned false → IsContractTx gated every CALL (To != nil) onto
			// the value-transfer path and the EVM never ran for contract calls
			// (deploys bypass hasCode, which is why only deploys worked).
			contractRepo := contractDB.NewKVStateRepository(cas.KV(), cas)
			evmexec.Register(
				cfg.Network.ChainID,
				DB_OPs.ContractAccountSource{},
				contractRepo,
				func(addr common.Address) bool {
					code, err := contractRepo.GetCode(context.Background(), addr)
					return err == nil && len(code) > 0
				},
			)
			// P4: fold contract state into the P2.5 fingerprint so the
			// halt-on-divergence check covers contract storage, not just accounts.
			kvStore := cas.KV()
			DB_OPs.SetContractFoldHook(func(f *consensushash.StateFingerprinterV1) error {
				return contractDB.FoldAllContracts(kvStore, f)
			})
			mainLogger().Info(context.Background(), "EVM contract execution ENABLED — execbridge executor + state-fingerprint contract fold registered")
		}

		// Construct the ThebeGateway (2PC writes + outbox retry) backing the
		// process-wide handle factory below. (The legacy ThebeShadowWriter hook
		// was removed with DualDB — migration Phase 7.)
		outbox, err := thebegateway.NewOutboxStore(cfg.Thebe.KVPath + "/outbox.db")
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL thebedb: outbox store init failed: %v\n", err)
			os.Exit(1)
		}
		gw := thebegateway.NewThebeGateway(builder.New(db), db.KV, nil, outbox)

		// Drain the outbox: any 2PC write failure is enqueued as a WAL row and
		// MUST be retried by this worker — without it, failed writes are
		// recorded and then never replayed (silent permanent write loss under
		// DB pressure; review finding R2).
		outboxWorker := thebegateway.NewOutboxWorker(outbox, gw, 5*time.Second)
		outboxWorker.Start()
		defer outboxWorker.Stop()

		// Wire the process-wide ThebeHandle factory. Every pool connection becomes a
		// cache-decorated store.ThebeHandle backed by ThebeDB: writes via the gateway
		// (2PC SQL+KV), reads via the reader (SQL). Pools are lazy, so setting this
		// before the first GetConnection is sufficient.
		reader := thebegateway.NewThebeReader(db.SQL.GetDB(), db.KV, nil)
		thebeHandleBackend := backend.New(gw, reader, nil)
		config.SetGlobalHandleFactory(func() (io.Closer, error) {
			return backend.NewComposite(thebeHandleBackend, nil), nil
		})
		// Also wire the process-wide ThebeHandle used by DB_OPs shim functions
		// (GetLatestBlockNumber, GetBlock, etc.) which call getHandle() directly
		// without going through a PooledConnection.
		DB_OPs.SetGlobalHandle(backend.NewComposite(thebeHandleBackend, nil))
		fmt.Fprintln(os.Stderr, "thebedb: gateway + handle factory enabled")

		// Genesis allocation (bootstrap / 2-node determinism gate). If
		// JMDN_GENESIS_ALLOC names a JSON {"0xADDR":"balanceWei"} file, seed those
		// accounts now — before any block is produced or applied — so the fleet's
		// pre-genesis baseline is identical. No-op when the env var is unset.
		// Requires JMDN_ALLOW_LOCAL_ACCOUNT_CREATE=1; idempotent; deterministic
		// (the P2.5 state fingerprint excludes ART ordinals + volatile timestamps).
		// FAIL CLOSED: a partial/failed genesis seed produces a divergent pre-genesis
		// baseline, which (with contracts enabled) lands the fleet in the P2.5 halt
		// path. Refuse to boot rather than run with a wrong baseline. This only fires
		// when JMDN_GENESIS_ALLOC is set — unset is a no-op (0, nil) and never fatal.
		if n, gErr := DB_OPs.SeedGenesisFromEnv(context.Background()); gErr != nil {
			log.Fatal().Err(gErr).Msg("[genesis] allocation seed failed — refusing to boot with a divergent baseline")
		} else if n > 0 {
			log.Info().Int("accounts", n).Msg("[genesis] allocation seeded")
		}

		// Genesis BLOCK (devnet/bootstrap): on a fresh chain, write an empty block 0
		// so "latest"-anchored reads (eth_getBalance, explorer, the orchestrator's
		// balance validation) resolve and the first produced block links to a real
		// parent. Only when JMDN_GENESIS_ALLOC is set (i.e. this is a seeded devnet) —
		// production chains get their blocks from the sequencer and must not synthesize
		// a genesis block. Idempotent (no-op once any block exists).
		if strings.TrimSpace(os.Getenv("JMDN_GENESIS_ALLOC")) != "" {
			if created, gbErr := DB_OPs.SeedGenesisBlockIfEmpty(context.Background()); gbErr != nil {
				log.Fatal().Err(gbErr).Msg("[genesis] block-0 seed failed — refusing to boot")
			} else if created {
				log.Info().Msg("[genesis] block 0 written (fresh chain)")
			}
		}
	}

	// Explorer stats account/DID counter. The stats API used to scan immudb
	// (CountAccounts, O(n)) on every request; instead the count is maintained in
	// the txindex sqlite. Increments are applied asynchronously so the
	// account-write path never blocks on the sqlite counter.
	DB_OPs.SetAccountCreatedHook(func(delta int) {
		go func() {
			if err := txindex.IncrAccountCount(context.Background(), int64(delta)); err != nil {
				log.Debug().Err(err).Int("delta", delta).Msg("[stats] account counter increment failed")
			}
		}()
	})
	// One-time seed: indexing the existing DIDs/accounts is a one-shot activity.
	// Only run the expensive immudb Count if the counter has never been seeded;
	// once present it is maintained by the increments above. Runs in a goroutine
	// so a first-boot seed never delays startup, and retries with backoff: the
	// immudb Count over the accounts prefix can exceed the default 30s on a large
	// DB or under load, so the seed uses a long per-attempt deadline and keeps
	// retrying until it succeeds (or the node shuts down).
	go func() {
		if _, seeded, err := txindex.GetAccountCount(context.Background()); err != nil {
			log.Debug().Err(err).Msg("[stats] account counter unavailable; skipping one-time seed")
			return
		} else if seeded {
			return // already indexed once — keep incrementing
		}
		backoff := 30 * time.Second
		for attempt := 1; ; attempt++ {
			// Re-check: another path (e.g. a manual reseed) may have seeded it.
			if _, seeded, _ := txindex.GetAccountCount(context.Background()); seeded {
				return
			}
			// Long per-attempt deadline — this runs off the request path.
			n, err := DB_OPs.CountAccountsWithTimeout(5 * time.Minute)
			if err == nil {
				if serr := txindex.SetAccountCount(context.Background(), int64(n)); serr != nil {
					log.Warn().Err(serr).Msg("[stats] failed to persist seeded account/DID count")
				} else {
					log.Info().Int("count", n).Int("attempt", attempt).Msg("[stats] account/DID counter seeded (one-time)")
					return
				}
			} else {
				log.Warn().Err(err).Int("attempt", attempt).Dur("retry_in", backoff).
					Msg("[stats] one-time account/DID count seed failed; retrying")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 10*time.Minute {
				backoff *= 2
			}
		}
	}()

	// ── Account Sync Worker (Redis Stream) ───────────────────────────────────
	// WriteAccounts and BatchUpdateAccounts enqueue to a Redis Stream and return
	// immediately, decoupling callers from synchronous DB commit latency.
	// The worker drains the stream and writes batches to ThebeDB asynchronously.
	// Required before FastsyncV2 starts — it calls WriteAccounts during sync.
	if cfg.Database.Redis.URL == "" {
		log.Warn().Msg("[AccountSyncWorker] database.redis.url not configured — WriteAccounts/BatchUpdateAccounts will fall back to synchronous direct ThebeDB writes (no async queue); set url in jmdn.yaml or JMDN_DATABASE_REDIS_URL to enable the async Redis-backed path")
	} else {
		redisClient := redis.NewClient(&redis.Options{
			Addr:     cfg.Database.Redis.URL,
			Password: cfg.Database.Redis.Password,
		})
		accountStreamer := NodeInfo.NewRedisStreamer(redisClient)
		NodeInfo.StartAccountSyncWorker(logger_ctx, accountStreamer, NodeInfo.DefaultWorkerConfig())
		log.Info().Str("redis_url", cfg.Database.Redis.URL).Msg("[accountqueue] installed — WriteAccounts is now async, worker starts lazily")
	}

	// Discover Yggdrasil address BEFORE creating the node
	fmt.Println("Discovering Yggdrasil address...")
	ipv6, err := helper.GetTun0GlobalIPv6()
	if err != nil || ipv6 == "" {
		ipv6 = "?"
		log.Printf("Error getting Yggdrasil IPv6 address: %v", err)
	}
	config.Yggdrasil_Address = ipv6
	fmt.Println(config.ColorGreen+"Yggdrasil Global IPv6 Address:"+config.ColorReset, ipv6)

	// Start the node
	fmt.Println("Creating libp2p node...")
	n, err := node.NewNode(logger_ctx)
	if err != nil {
		fmt.Println("Error starting node:", err)
		return
	}
	defer n.Host.Close()
	fmt.Println("Node created successfully")

	// Set the host instance for broadcast messaging
	messaging.SetHostInstance(n.Host)
	profiler.RegisterHost(n.Host)

	// Initialize the listener node for handling submit message protocol
	// This sets up the SubmitMessageProtocol handler for vote submission
	listener := MessagePassing.NewListenerNode(logger_ctx, n.Host, Sequencer.NewResponseHandler())
	fmt.Printf("✅ Message listener initialized with ID: %s\n", listener.ListenerBuddyNode.PeerID.String())

	// Initialize PubSub system
	globalPubSub, err := initPubSub(n)
	if err != nil {
		fmt.Printf("Failed to initialize PubSub system: %v\n", err)
		mainLogger().Error(context.Background(), "Failed to initialize PubSub system", err)
		// Continue without PubSub - some features may be limited
	} else {
		fmt.Println("✅ PubSub system ready for consensus and messaging")
		log.Info().Msg("PubSub system initialized successfully")
		// Give Block server access to GPS so /api/l1-commit can broadcast to peers.
		Block.SetGossipPubSubInstance(globalPubSub.GetGossipPubSub())

		// Wire additive finalized-block gossip fan-out. Publish is used only by the
		// sequencer's finalize path; subscribe+apply runs on every node so finalized
		// blocks reach the whole fleet (not just the sequencer's connected committee),
		// each verified through the same fail-closed admitZKBlock gate.
		blockgossip.Start(ctx, globalPubSub.GetGossipPubSub())

		// Subscribe to the dedicated L1 commit channel at startup so this node
		// receives L1Commit/L1CommitRange broadcasts from the sequencer. This
		// topic is persistent — unlike the consensus channel, it is never
		// unsubscribed at the end of a consensus round (END_PUBSUB).
		go func() {
			svc := MsgPassingService.NewSubscriptionService(globalPubSub.GetGossipPubSub())
			if subErr := svc.HandleStreamSubscriptionRequest(ctx, config.PubSub_L1CommitChannel); subErr != nil {
				fmt.Printf("⚠️  Failed to subscribe to L1 commit channel at startup: %v\n", subErr)
			} else {
				fmt.Println("✅ Subscribed to L1 commit channel at startup")
			}
		}()
	}

	// Legacy pool-client acquisition removed: storage goes through the
	// process-wide ThebeHandle (config.SetGlobalHandleFactory above); helpers
	// take a nil conn and resolve getHandle(nil) internally.

	// Sync engine: ThebeSync (FastSync v4). FastsyncV2 is retired — serving is via
	// the thebesync handlers registered in node.go, catch-up via thebesync.CatchUp.
	if !cfg.FastSync.Enabled {
		log.Info().Msg("[Sync] disabled by config — sync monitor not started")
	}

	// SeedMonitor: every node with sync enabled reports its Merkle root to the
	// seednode periodically (outbound only, no DB writes ever).
	// ReconcileFunc is only wired when enable_catchup=true — never set on the sequencer.
	// enable_pulling guards CLI pull commands and the reconcile path independently.
	var syncMonitor *syncmonitor.Monitor
	if cfg.FastSync.Enabled {
		if cfg.Network.SeedNode == "" {
			log.Warn().Msg("[SyncMonitor] cfg.network.seed_node not set — sync monitor disabled")
		} else {
			selfPeerID := n.Host.ID().String()
			seedCli, err := seednode.NewClient(cfg.Network.SeedNode)
			if err != nil {
				log.Error().Err(err).Msg("[SyncMonitor] failed to create seednode client — sync monitor disabled")
			} else {
				blockInfo := NodeInfo.NewSyncStruct()
				reporter := syncmonitor.NewSeednodeReporter(seedCli, selfPeerID)
				syncMonitor = syncmonitor.New(blockInfo, reporter, cfg.FastSync.SyncCheckInterval)

				// Only wire reconciliation on non-sequencer nodes.
				// The sequencer sets enable_catchup=false — it is the authoritative source,
				// it never catches up from peers.
				if cfg.FastSync.EnableCatchup {
					fromBlock := cfg.FastSync.CatchUpFromBlock
					syncMonitor.SetReconcileFunc(func(rctx context.Context, peers []syncmonitor.PeerInfo) error {
						if len(peers) == 0 {
							return fmt.Errorf("[ReconcileFunc] seednode returned no good peers")
						}
						for _, p := range peers {
							if len(p.Multiaddrs) == 0 {
								log.Warn().Str("peer", p.PeerID).Msg("[ReconcileFunc] peer has no multiaddrs, skipping")
								continue
							}
							targetMultiaddr := p.Multiaddrs[0] + "/p2p/" + p.PeerID
							log.Info().
								Str("peer", p.PeerID).
								Str("addr", targetMultiaddr).
								Uint64("from_block", fromBlock).
								Msg("[ReconcileFunc] attempting catchup")
							// ThebeSync (FastSync v4) log-shipping catch-up replaces the
							// FastsyncV2 Merkle-bisection path. It auto-detects the range
							// from the local tip, so fromBlock is no longer passed.
							if _, err := thebesync.CatchUp(rctx, n.Host, targetMultiaddr); err != nil {
								log.Warn().Err(err).Str("peer", p.PeerID).Msg("[ReconcileFunc] peer failed, trying next")
								continue
							}
							log.Info().Str("peer", p.PeerID).Msg("[ReconcileFunc] catchup succeeded")
							return nil
						}
						return fmt.Errorf("[ReconcileFunc] all %d seednode peers failed catchup", len(peers))
					})

					// When the block-propagation linkage check detects a height
					// gap, nudge the monitor to run an immediate authenticated
					// reconcile (seednode-vetted peers) instead of waiting for the
					// next periodic tick. Best-effort; the gap block is rejected
					// regardless.
					localMonitor := syncMonitor
					messaging.SetCatchUpRequester(func(fromBlock uint64) {
						if localMonitor == nil {
							return
						}
						log.Info().Uint64("from_block", fromBlock).Msg("height gap detected — triggering authenticated catch-up")
						go localMonitor.TriggerCheck(context.Background())
					})
				}

				if err := syncMonitor.Start(ctx); err != nil {
					log.Error().Err(err).Msg("[SyncMonitor] failed to start — continuing without monitor")
					syncMonitor = nil
					if closeErr := seedCli.Close(); closeErr != nil {
						log.Warn().Err(closeErr).Msg("[SyncMonitor] seednode client close error")
					}
				} else {
					log.Info().
						Bool("catchup", cfg.FastSync.EnableCatchup).
						Dur("interval", cfg.FastSync.SyncCheckInterval).
						Msg("[SyncMonitor] started")

					// Event-driven seed reporting: push this node's head to the
					// seednode immediately after a block's state is committed,
					// instead of waiting for the periodic monitor tick. The hook
					// only signals a debounced, async pusher (never blocks the
					// apply path); the periodic timer remains the backstop.
					localMon := syncMonitor
					DB_OPs.SetLatestBlockAdvanceHook(startSeedBlockHeadPusher(ctx, func(c context.Context) {
						localMon.TriggerCheck(c)
					}))
					log.Info().Msg("[SeedPush] event-driven block-head reporting wired")
				}
			}
		}
	}

	// Wire the consensus vote gate on every node: a node may be a buddy / cast a
	// vote only if it holds the latest block or trails the sequencer head by at
	// most MessagePassing.MaxConsensusLagBlocks (2); a fresh node (confirmed tip 0)
	// or one 3+ blocks behind must not. On a DB read ERROR the local tip is UNKNOWN
	// (not confirmed-behind) → PERMIT (fail-open): now that the gate is
	// default-ON, a transient read hiccup must not pull a validator out of consensus
	// and stall quorum. The sequencer runs no monitor, so its
	// head is "unknown" here and it votes on the strength of its non-empty chain (it
	// IS the head). headKnown is false during a seednode outage (SeednodeUnreachable)
	// so a transient loss of the head reference does not stall consensus — only a
	// KNOWN gap > 2 abstains. Captured syncMonitor may be nil (no fastsync/seednode).
	gateMonitor := syncMonitor
	MessagePassing.SetConsensusSyncGate(func() bool {
		tip, err := DB_OPs.GetLatestBlockNumber(context.Background(), nil)
		localTipKnown := err == nil
		if err != nil {
			log.Warn().Err(err).Msg("[consensus gate] local tip read failed — permitting vote (fail-open on UNKNOWN local state; confirmed-empty tip 0 still abstains)")
		}
		if gateMonitor == nil {
			return MessagePassing.GateDecision(localTipKnown, tip, 0, false)
		}
		st := gateMonitor.GetStatus()
		headKnown := st.SequencerHead > 0 && !st.SeednodeUnreachable
		return MessagePassing.GateDecision(localTipKnown, tip, st.SequencerHead, headKnown)
	})

	// Committee-eligibility source on validator (non-sequencer) nodes. "Validator"
	// is keyed off enable_catchup — validators catch up from peers (true); the
	// sequencer is the authoritative producer and sets it false. This is the SAME
	// discriminator the sync monitor uses above. It is deliberately NOT keyed off
	// the block-generator port, which is set on validators too (fleet-wide), not
	// only on the sequencer — keying off BlockGen silently skipped every validator
	// that runs the block-gen API.
	//
	// The sequencer wires its own pinned source in Sequencer.NewConsensus (called
	// only from the block-production path) and is left untouched here. Every
	// validator needs a source too: the mandatory block-certificate check in
	// admitZKBlock calls messaging.VerifyCertificate, which fails CLOSED without
	// one — so a receiver with no source drops (and stops forwarding) every block.
	// Authority key: the operator pin if set, else trust-on-first-use of the
	// seed-served key (persisted to config/seedAuth.json; override with
	// JMDN_SEED_AUTH_FILE). The verified snapshot is cached, so the seed is queried
	// about once per refresh window, not per block.
	if cfg.FastSync.EnableCatchup && cfg.Network.SeedNode != "" {
		if elCli, err := seednode.NewClient(cfg.Network.SeedNode); err != nil {
			log.Error().Err(err).
				Msg("[Committee] seed client init failed — certificate verification stays fail-closed until a source is available")
		} else {
			messaging.SetCommitteeEligibilitySource(elCli.CommitteeEligibilityAuto(
				cfg.Consensus.SeedAuthorityBLSPub,
				cfg.Consensus.CommitteeEpochSeconds,
				seednode.SeedAuthPinPath(),
				cfg.Network.SeedNode,
				60*time.Second,
			))
			log.Info().Msg("[Committee] eligibility source wired on non-sequencer node (pin-or-TOFU committee snapshot)")
		}
	}

	// Initialize Yggdrasil messaging if enabled
	if cfg.Network.Yggdrasil {
		initYggdrasilMessaging(ctx)
		mainLogger().Info(context.Background(), fmt.Sprintf("Yggdrasil messaging enabled on port %d", directMSG.YggdrasilPort))
	}

	// Display node identity
	fmt.Println(config.ColorGreen+"Yggdrasil Global IPv6 Full Peer Address:"+config.ColorReset, "/ip6/"+config.Yggdrasil_Address+"/tcp/15000/p2p/"+n.Host.ID().String())

	fmt.Println(config.ColorGreen+"Node ID:"+config.ColorReset, n.Host.ID().String())
	fmt.Println("Addresses:")
	for _, addr := range n.Host.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, n.Host.ID().String())
	}

	if cfg.Network.Mempool == "" {
		log.Printf("No mempool gRPC address provided; cannot proceed.")
		return
	}

	address := cfg.Network.Mempool
	if err := Block.InitMempoolClient(logger_ctx, address); err != nil {
		log.Printf("Failed to connect to mempool: %v", err)
	}
	defer Block.CloseMempoolClient()

	// Initialize routing client to the same address as mempool
	_, err = Block.NewRoutingServiceClient(address)
	if err != nil {
		log.Printf("Failed to connect to routing service: %v", err)
	} else {
		log.Printf("Routing client initialized successfully")
	}

	// Transaction-status resolution (default-off). Wired here, after the routing
	// client exists and before StartFacadeServer, so the RPC facade sees a
	// resolver that can already reach the mempool.
	initTxStatus(cfg)

	// Initialize node manager
	nodeManager, err = node.NewNodeManagerWithLogger(n)
	if err != nil {
		fmt.Printf("Failed to initialize node manager: %v\n", err)
		return
	}
	// Debugging
	fmt.Println("Node manager initialized successfully")

	nodeManager.StartHeartbeat(cfg.Network.HeartbeatInterval)
	defer nodeManager.Shutdown()

	// Initialize DID propagation handler
	n.Host.SetStreamHandler(config.DIDPropagationProtocol, messaging.HandleDIDStream)

	// Initialize DID propagation system
	if err := messaging.InitDIDPropagation(nil); err != nil {
		fmt.Printf("Failed to initialize DID propagation: %v\n", err)
		mainLogger().Error(context.Background(), "Failed to initialize DID propagation", err)
	}

	// Initialize Contract propagation handler (ADR-001)
	n.Host.SetStreamHandler(config.ContractPropagationProtocol, messaging.HandleContractStream)
	// Pull-on-demand: peers can request missed contract metadata from us
	n.Host.SetStreamHandler(config.ContractPullProtocol, messaging.HandleContractPullStream)

	if err := messaging.InitContractPropagation(); err != nil {
		mainLogger().Error(context.Background(), "Failed to initialize contract propagation", err)
	}

	// We'll initialize the DID system in the DID server to avoid blocking main
	// Start DID server only when port > 0 (optional on non-resolver nodes)
	if cfg.Ports.DID > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.DIDThread, func(ctx context.Context) error {
			didAddr := fmt.Sprintf("%s:%d", cfg.Binds.DID, cfg.Ports.DID)
			mainLogger().Info(context.Background(), "Starting DID gRPC server", ion.String("address", didAddr))
			if err := startDIDServer(ctx, n.Host, didAddr); err != nil {
				fmt.Println("Failed to start DID gRPC server:", err)
				mainLogger().Error(context.Background(), "Failed to start DID gRPC server", err)
			}
			return nil
		}); err != nil {
			mainLogger().Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.DIDThread))
		}
	}

	if cfg.Ports.BlockGen > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.BlockgenThread, func(ctx context.Context) error {
			mainLogger().Info(context.Background(), fmt.Sprintf("Starting block generator on port %d", cfg.Ports.BlockGen))
			fmt.Printf("\nBlock generator available at http://localhost:%d\n", cfg.Ports.BlockGen)
			if err := Block.StartserverWithContext(ctx, cfg.Binds.BlockGen, cfg.Ports.BlockGen, n.Host, cfg.Network.ChainID); err != nil {
				mainLogger().Error(context.Background(), "Block generator server stopped", err)
			}
			return nil
		}); err != nil {
			mainLogger().Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.BlockgenThread))
		}
	}

	if cfg.Ports.BlockGRPC > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.BlockgRPCThread, func(ctx context.Context) error {
			mainLogger().Info(context.Background(), "Starting block gRPC server", ion.Int("port", cfg.Ports.BlockGRPC))
			fmt.Printf("\nBlock gRPC server available at localhost:%d\n", cfg.Ports.BlockGRPC)
			if err := Block.StartGRPCServer(cfg.Ports.BlockGRPC, n.Host, cfg.Network.ChainID); err != nil {
				mainLogger().Error(context.Background(), "Failed to start block gRPC server", err)
			}
			return nil
		}); err != nil {
			mainLogger().Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.BlockgRPCThread))
		}
	}

	// Start internal gETH server if port > 0
	if cfg.Ports.Geth > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.GETHgRPCThread, func(ctx context.Context) error {
			mainLogger().Info(context.Background(), "Starting internal gETH gRPC server", ion.Int("port", cfg.Ports.Geth))
			if err := gETH.StartGRPC(cfg.Ports.Geth, cfg.Network.ChainID); err != nil {
				mainLogger().Error(context.Background(), "Failed to start gETH gRPC server", err)
			}
			return nil
		}); err != nil {
			mainLogger().Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.GETHgRPCThread))
		}
	}

	// Start integrated Smart Contract server if port > 0
	if cfg.Ports.Smart > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.SmartContractThread, func(ctx context.Context) error {
			mainLogger().Info(context.Background(), "Starting integrated Smart Contract gRPC server", ion.Int("port", cfg.Ports.Smart))
			didAddr := fmt.Sprintf("%s:%d", cfg.Binds.DID, cfg.Ports.DID)
			if err := SmartContract.StartIntegratedServer(ctx, cfg.Ports.Smart, cfg.Network.ChainID, cfg.Ports.Geth, didAddr, cfg.Ports.BlockGen, cas); err != nil {
				mainLogger().Error(context.Background(), "Failed to start Smart Contract integrated server", err)
			}
			return nil
		}); err != nil {
			mainLogger().Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.SmartContractThread))
		}
	}

	// Register with seed node gRPC if URL is provided
	if cfg.Network.SeedNode != "" {
		fmt.Printf("Registering with seed node gRPC: %s\n", cfg.Network.SeedNode)
		seedClient, err := seednode.NewClient(cfg.Network.SeedNode)
		if err != nil {
			fmt.Printf("Failed to create seed node client: %v\n", err)
			mainLogger().Error(context.Background(), "Failed to create seed node client", err)
		} else {
			defer seedClient.Close()

			// Register this peer with the seed node (with or without alias)
			if cfg.Node.Alias != "" {
				fmt.Printf("Registering with alias: %s\n", cfg.Node.Alias)
				err = seedClient.RegisterPeerWithAlias(n.Host, cfg.Node.Alias)
				if err != nil {
					fmt.Printf("Failed to register with seed node using alias: %v\n", err)
					mainLogger().Error(context.Background(), "Failed to register with seed node using alias", err)
				} else {
					fmt.Printf("Successfully registered with seed node using alias '%s'\n", cfg.Node.Alias)
					mainLogger().Info(context.Background(), "Successfully registered with seed node using alias", ion.String("alias", cfg.Node.Alias))
				}
			} else {
				err = seedClient.RegisterPeer(n.Host)
				if err != nil {
					fmt.Printf("Failed to register with seed node: %v\n", err)
					mainLogger().Error(context.Background(), "Failed to register with seed node", err)
				} else {
					fmt.Println("Successfully registered with seed node")
					mainLogger().Info(context.Background(), "Successfully registered with seed node")
				}
			}

			// Perform neighbor discovery after successful registration
			fmt.Println("\n🔍 Starting neighbor discovery process...")
			err = seedClient.DiscoverAndAddNeighbors(n.Host, nodeManager)
			if err != nil {
				fmt.Printf("⚠️  Neighbor discovery failed: %v\n", err)
				mainLogger().Error(context.Background(), "Neighbor discovery failed", err)
			} else {
				fmt.Println("✅ Neighbor discovery completed successfully")
				mainLogger().Info(context.Background(), "Neighbor discovery completed successfully")
			}
		}
	}

	if cfg.Ports.API > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.ExplorerThread, func(ctx context.Context) error {
			mainLogger().Info(context.Background(), fmt.Sprintf("Starting Explorer API on port %d", cfg.Ports.API))
			fmt.Printf("\nExplorer API available at http://localhost:%d/api\n", cfg.Ports.API)

			// Initialize API server
			apiAddr := fmt.Sprintf("%s:%d", cfg.Binds.API, cfg.Ports.API)
			if err := StartAPIServer(ctx, apiAddr); err != nil {
				mainLogger().Error(context.Background(), "Failed to start API server", err)
			}
			return nil
		}); err != nil {
			mainLogger().Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.ExplorerThread))
		}
	}

	cmdHandler := &cli.CommandHandler{
		Node:            n,
		NodeManager:     nodeManager,
		SeedNode:        cfg.Network.SeedNode,
		EnableYggdrasil: cfg.Network.Yggdrasil,
		ChainID:         cfg.Network.ChainID,
		FacadePort:      cfg.Ports.Facade,
		WSPort:          cfg.Ports.WS,
		PullAllowed:     cfg.FastSync.EnablePulling,
	}

	if cfg.Ports.Facade > 0 {
		fmt.Printf("Starting gETH Facade server on port %d\n", cfg.Ports.Facade)
		StartFacadeServer(
			cfg.Binds.Facade,
			cfg.Ports.Facade,
			cfg.Binds.ThebeDebug,
			cfg.Ports.ThebeDebug,
			cfg.Network.ChainID,
			cfg.Ports.Smart,
			syncMonitor,
		)
	}

	if cfg.Ports.WS > 0 {
		fmt.Printf("Starting gETH WSServer on port %d\n", cfg.Ports.WS)
		StartWSServer(cfg.Binds.WS, cfg.Ports.WS, cfg.Network.ChainID, cfg.Ports.Smart)
	}

	// Start CLI without timeout - run indefinitely
	// Only start CLI when port > 0 (disabled by default per jmdn_default.yaml)
	done := make(chan error, 1)
	if cfg.Ports.CLI > 0 {
		if err := goMaybeTracked(MainLM, GRO.MainAM, GRO.MainLM, GRO.CLIThread, func(ctx context.Context) error {
			done <- cmdHandler.StartCLI(ctx, cfg.Binds.CLI, cfg.Ports.CLI)
			return nil
		}); err != nil {
			mainLogger().Error(context.Background(), "Failed to start GRO goroutine", err, ion.String("thread", GRO.CLIThread))
			done <- err
		}

		// Wait for CLI to complete or error
		if err := <-done; err != nil {
			mainLogger().Error(context.Background(), "Failed to start CLI", err)
		}
	} else {
		mainLogger().Info(context.Background(), "CLI server disabled (port = 0)")
		// Keep the node running even without CLI
		select {}
	}
}
