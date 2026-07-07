package CLI

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/txindex"
	"gossipnode/config"
	"gossipnode/helper"
	"gossipnode/messaging/directMSG"
	"gossipnode/node"
	"gossipnode/seed"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

type HandlePeers struct {
	Num           int
	PeerID        peer.ID
	Multiaddr     string
	HeartbeatFail int
	IsAlive       bool
	Status        string
	LastSeen      string
}

type resp struct {
	Total int
	Peers []HandlePeers
	Error string
}

type HandleShowStats struct {
	MessagesSent     int64
	MessagesReceived int64
	MessagesFailed   int64
}

type SyncStats struct {
	TimeTaken     time.Duration
	MainState     *DB_OPs.DatabaseState
	AccountsState *DB_OPs.DatabaseState
	Error         string
}

type HandleAddrs struct {
	Total int
	Peers []string
	Error string
}

func (h *CommandHandler) ReturnAddrs() (HandleAddrs, error) {
	ipv6, err := helper.GetTun0GlobalIPv6()
	if err != nil || ipv6 == "" {
		ipv6 = "?"
	}
	addrs := make([]string, 0)
	yggdrasilAddr := "/ip6/" + ipv6 + "/tcp/15000/p2p/" + h.Node.Host.ID().String()
	addrs = append(addrs, yggdrasilAddr)

	for _, addr := range h.Node.Host.Addrs() {
		addrs = append(addrs, addr.String()+"/p2p/"+h.Node.Host.ID().String())
	}
	return HandleAddrs{
		Total: len(addrs),
		Peers: addrs,
		Error: "",
	}, nil
}

// Individual command handlers
func (h *CommandHandler) HandleSendMessage(peer string, message string) (bool, error) {
	if peer == "" || message == "" {
		return false, fmt.Errorf("usage: msg <peer_multiaddr> <message>")
	}
	err := node.SendMessage(h.Node, peer, message)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (h *CommandHandler) HandleYggdrasilMessage(peer string, message string) (bool, error) {
	if !h.EnableYggdrasil {
		return false, fmt.Errorf("yggdrasil messaging is disabled. Start with -ygg flag to enable")
	}
	if peer == "" || message == "" {
		return false, fmt.Errorf("usage: ygg <peer_multiaddr|ygg_ipv6> <message>")
	}
	err := directMSG.SendYggdrasilMessage(peer, message)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (h *CommandHandler) HandleSendFile(peer string, filepath string, remote_filename string) (bool, error) {
	if peer == "" || filepath == "" || remote_filename == "" {
		return false, fmt.Errorf("usage: file <peer_multiaddr> <filepath>")
	}
	err := node.SendFile(h.Node, peer, filepath, remote_filename)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (h *CommandHandler) HandleRequestPeers_fromSeeds(seedNode string) (bool, []config.PeerInfo, error) {
	if seedNode == "" {
		return false, nil, fmt.Errorf("no seed node specified. Use -connect flag to specify a seed node")
	}

	peers, err := seed.RequestPeers(h.Node.Host, seedNode, 20, "")
	if err != nil {
		return false, nil, err
	} else {
		return true, peers, nil
	}
}

func (h *CommandHandler) HandleAddPeer(peer string) (bool, error) {
	if peer == "" {
		return false, fmt.Errorf("usage: addpeer <peer_multiaddr>")
	}
	err := h.NodeManager.AddPeer(peer)
	if err != nil {
		return false, err
	} else {
		return true, nil
	}
}

func (h *CommandHandler) HandleRemovePeer(peer string) (bool, error) {
	if peer == "" {
		return false, fmt.Errorf("usage: removepeer <peer_id>")
	}
	err := h.NodeManager.RemovePeer(peer)
	if err != nil {
		return false, err
	} else {
		return true, nil
	}
}

func (h *CommandHandler) HandleListPeers() (resp, error) {

	peers := h.NodeManager.ListManagedPeers()
	var list []HandlePeers

	for i, p := range peers {
		status := "ONLINE"
		if !p.IsAlive {
			status = "OFFLINE"
		}
		lastSeen := time.Unix(p.LastSeen, 0).Format(time.RFC3339)
		list = append(list, HandlePeers{
			Num:           i + 1,
			PeerID:        p.ID,
			Multiaddr:     p.Multiaddr,
			HeartbeatFail: p.HeartbeatFail,
			IsAlive:       p.IsAlive,
			Status:        status,
			LastSeen:      lastSeen,
		})
	}

	return resp{
		Total: len(peers),
		Peers: list,
		Error: "",
	}, nil
}

func (h *CommandHandler) HandleCleanPeers() (int, error) {
	cleaned, err := h.NodeManager.CleanupOfflinePeers(9) // Remove peers with 9+ failures
	if err != nil {
		return 0, err
	} else {
		return cleaned, nil
	}
}

func (h *CommandHandler) HandleShowStats() (HandleShowStats, error) {
	if h.EnableYggdrasil {
		stats := directMSG.GetMetrics()
		return HandleShowStats{
			MessagesSent:     stats["messages_sent"],
			MessagesReceived: stats["messages_received"],
			MessagesFailed:   stats["messages_failed"],
		}, nil
	} else {
		return HandleShowStats{}, fmt.Errorf("yggdrasil messaging is disabled")
	}
}

func (h *CommandHandler) HandleBroadcast(message string) (bool, error) {
	if message == "" {
		return false, fmt.Errorf("usage: broadcast <message>")
	}
	err := node.BroadcastMessage(h.Node, message)
	if err != nil {
		return false, err
	} else {
		return true, nil
	}
}

func (h *CommandHandler) CheckDBStats() (*DB_OPs.DatabaseState, *DB_OPs.DatabaseState, error) {
	// Get both database states before sync
	mainState, err := DB_OPs.GetDatabaseState(h.MainClient)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get main database state: %v", err)
	}

	accountsState, err := DB_OPs.GetDatabaseState(h.DIDClient)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get accounts database state: %v", err)
	}
	return mainState, accountsState, nil
}

func (h *CommandHandler) HandleFastSync(peeraddr string) (SyncStats, error) {
	if peeraddr == "" {
		return SyncStats{}, fmt.Errorf("usage: fastsync <peer_multiaddr>")
	}
	if !h.PullAllowed {
		return SyncStats{}, fmt.Errorf("node is configured as a serve-only participant (pulling disabled). cannot pull data")
	}

	err := h.checkDBClient()
	if err != nil {
		return SyncStats{}, fmt.Errorf("database client not initialized: %v", err)
	}

	err = h.checkDIDClient()
	if err != nil {
		return SyncStats{}, fmt.Errorf("database client (DID) not initialized: %v", err)
	}

	// Parse the multiaddr
	addr, err := ma.NewMultiaddr(peeraddr)
	if err != nil {
		return SyncStats{}, fmt.Errorf("invalid multiaddress: %v", err)
	}

	// Extract peer ID from multiaddr
	addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to extract peer info: %v", err)
	}

	fmt.Printf("Starting blockchain sync with peer %s\n", addrInfo.ID.String())

	// Start the sync process
	startTime := time.Now().UTC()

	maxRetries := 3
	var syncErr error

	for retry := 0; retry < maxRetries; retry++ {
		if retry > 0 {
			fmt.Printf("Retry %d/%d after error: %v\n", retry+1, maxRetries, syncErr)
			time.Sleep(2 * time.Second)
		}

		// Legacy fastsync removed in the ThebeDB migration; route to the V2 engine.
		if h.FastSyncerV2 == nil {
			return SyncStats{}, fmt.Errorf("FastsyncV2 engine is inactive")
		}
		syncErr = h.FastSyncerV2.HandleSync(peeraddr)
		if syncErr == nil {
			break
		}
	}

	if syncErr != nil {
		return SyncStats{}, fmt.Errorf("sync failed after %d attempts: %v", maxRetries, syncErr)
	}

	// Get post-sync states
	newMainState, err := DB_OPs.GetDatabaseState(h.MainClient)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to get main database state after sync: %v", err)
	}

	newAccountsState, err := DB_OPs.GetDatabaseState(h.DIDClient)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to get accounts database state after sync: %v", err)
	}

	return SyncStats{
		TimeTaken:     time.Since(startTime),
		MainState:     newMainState,
		AccountsState: newAccountsState,
	}, nil
}

func (h *CommandHandler) HandleFastSyncV2(peeraddr string) (SyncStats, error) {
	return SyncStats{}, fmt.Errorf("fastsync removed: use ThebeDB sync instead")
}

func (h *CommandHandler) HandleCatchUpSync(ctx context.Context, peeraddr string, fromBlock uint64) (SyncStats, error) {
	if peeraddr == "" {
		return SyncStats{}, fmt.Errorf("usage: catchup <peer_multiaddr> [from_block]")
	}
	// fromBlock=0 → auto-detect via effectiveReconRange inside HandleCatchUpSync
	if !h.PullAllowed {
		return SyncStats{}, fmt.Errorf("node is configured as a serve-only participant (pulling disabled). cannot pull data")
	}
	if h.FastSyncerV2 == nil {
		return SyncStats{}, fmt.Errorf("FastsyncV2 engine is inactive")
	}

	startTime := time.Now().UTC()
	if err := h.FastSyncerV2.HandleCatchUpSync(ctx, fromBlock, peeraddr); err != nil {
		return SyncStats{}, fmt.Errorf("CatchUpSync failed: %w", err)
	}

	var newMainState, newAccountsState *DB_OPs.DatabaseState
	if h.MainClient != nil {
		newMainState, _ = DB_OPs.GetDatabaseState(h.MainClient)
	}
	if h.DIDClient != nil {
		newAccountsState, _ = DB_OPs.GetDatabaseState(h.DIDClient)
	}

	return SyncStats{
		TimeTaken:     time.Since(startTime),
		MainState:     newMainState,
		AccountsState: newAccountsState,
	}, nil
}

func (h *CommandHandler) HandleAccountSync(peeraddr string) (SyncStats, error) {
	if peeraddr == "" {
		return SyncStats{}, fmt.Errorf("usage: accountsync <peer_multiaddr>")
	}
	if !h.PullAllowed {
		return SyncStats{}, fmt.Errorf("node is configured as a serve-only participant (pulling disabled). cannot pull data")
	}
	if h.FastSyncerV2 == nil {
		return SyncStats{}, fmt.Errorf("FastsyncV2 engine is inactive")
	}

	startTime := time.Now().UTC()
	err := h.FastSyncerV2.HandleSync(peeraddr)
	if err != nil {
		return SyncStats{}, fmt.Errorf("AccountSync failed: %w", err)
	}

	var newAccountsState *DB_OPs.DatabaseState
	if h.DIDClient != nil {
		newAccountsState, _ = DB_OPs.GetDatabaseState(h.DIDClient)
	}

	return SyncStats{
		TimeTaken:     time.Since(startTime),
		AccountsState: newAccountsState,
	}, nil
}

func (h *CommandHandler) HandleFirstSync(peeraddr string, mode string) (SyncStats, error) {
	if peeraddr == "" {
		return SyncStats{}, fmt.Errorf("usage: firstsync <peer_multiaddr> <server|client>")
	}

	if mode == "" {
		return SyncStats{}, fmt.Errorf("usage: firstsync <peer_multiaddr> <server|client>")
	}

	modeLower := strings.ToLower(mode)
	if modeLower == "client" && !h.PullAllowed {
		return SyncStats{}, fmt.Errorf("node is configured as a serve-only participant (pulling disabled). cannot pull data")
	}

	err := h.checkDBClient()
	if err != nil {
		return SyncStats{}, fmt.Errorf("database client not initialized: %v", err)
	}

	err = h.checkDIDClient()
	if err != nil {
		return SyncStats{}, fmt.Errorf("database client (DID) not initialized: %v", err)
	}

	// Parse the multiaddr
	addr, err := ma.NewMultiaddr(peeraddr)
	if err != nil {
		return SyncStats{}, fmt.Errorf("invalid multiaddress: %v", err)
	}

	// Extract peer ID from multiaddr
	addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to extract peer info: %v", err)
	}

	if modeLower != "server" && modeLower != "client" {
		return SyncStats{}, fmt.Errorf("invalid mode: %s. Must be 'server' or 'client'", mode)
	}

	fmt.Printf("Starting first sync with peer %s (mode: %s)\n", addrInfo.ID.String(), modeLower)
	startTime := time.Now().UTC()

	// Legacy fastsync (server/client first-sync split) removed in the ThebeDB
	// migration. FastsyncV2 performs a unified sync regardless of mode; the mode
	// argument is retained for CLI compatibility but is now informational only.
	if h.FastSyncerV2 == nil {
		return SyncStats{}, fmt.Errorf("FastsyncV2 engine is inactive")
	}
	fmt.Printf(">>> Running unified FastsyncV2 (requested mode: %s)...\n", modeLower)
	syncErr := h.FastSyncerV2.HandleSync(peeraddr)

	if syncErr != nil {
		return SyncStats{}, fmt.Errorf("first sync failed: %v", syncErr)
	}

	// Get post-sync states
	newMainState, err := DB_OPs.GetDatabaseState(h.MainClient)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to get main database state after sync: %v", err)
	}

	newAccountsState, err := DB_OPs.GetDatabaseState(h.DIDClient)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to get accounts database state after sync: %v", err)
	}

	return SyncStats{
		TimeTaken:     time.Since(startTime),
		MainState:     newMainState,
		AccountsState: newAccountsState,
	}, nil
}

// HandleRebuildIndex wipes and rebuilds the tx-address SQLite index from genesis.
// Fixes all gaps regardless of where last_indexed_block is sitting.
func (h *CommandHandler) HandleRebuildIndex(ctx context.Context) (time.Duration, error) {
	startTime := time.Now()
	if err := txindex.RebuildIndex(ctx); err != nil {
		return 0, fmt.Errorf("RebuildIndex failed: %w", err)
	}
	return time.Since(startTime), nil
}

// HandleRebuildRange re-indexes a specific block range [from, to].
// Safe to run over already-indexed blocks — INSERT OR IGNORE prevents duplicates.
func (h *CommandHandler) HandleRebuildRange(ctx context.Context, from, to uint64) (time.Duration, error) {
	if from > to {
		return 0, fmt.Errorf("from_block (%d) must be <= to_block (%d)", from, to)
	}
	startTime := time.Now()
	if err := txindex.RebuildRange(ctx, from, to); err != nil {
		return 0, fmt.Errorf("RebuildRange [%d..%d] failed: %w", from, to, err)
	}
	return time.Since(startTime), nil
}

// HandleTxIndexStatus reports whether the tx-address index has completed its
// first full gap catchup, and the highest block number it has indexed so far.
func (h *CommandHandler) HandleTxIndexStatus(ctx context.Context) (isReady bool, lastIndexedBlock uint64, err error) {
	return txindex.Status(ctx)
}

func (h *CommandHandler) HandleGetDID(input string) (*DB_OPs.Account, error) {
	if input == "" {
		return nil, fmt.Errorf("usage: getDID <did|address>")
	}

	if strings.HasPrefix(input, DB_OPs.DIDPrefix) {
		doc, err := DB_OPs.GetAccountByDID(h.MainClient, input)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve DID %s: %v", input, err)
		}
		return doc, nil
	}

	// Treat as Ethereum address
	doc, err := DB_OPs.GetAccount(h.MainClient, common.HexToAddress(input))
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve address %s: %v", input, err)
	}
	return doc, nil
}
