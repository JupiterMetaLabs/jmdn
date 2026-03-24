package CLI

import (
	"fmt"
	"strings"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/helper"
	"gossipnode/messaging/directMSG"
	"gossipnode/node"
	"gossipnode/seed"

	"github.com/codenotary/immudb/pkg/api/schema"
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
	MainState     *schema.ImmutableState
	AccountsState *schema.ImmutableState
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

// __DEAD_CODE_AUDIT_PUBLIC__
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

func (h *CommandHandler) CheckDBStats() (*schema.ImmutableState, *schema.ImmutableState, error) {
	// Get both database states before sync
	mainState, err := DB_OPs.GetDatabaseState(h.MainClient.Client)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get main database state: %v", err)
	}

	accountsState, err := DB_OPs.GetDatabaseState(h.DIDClient.Client)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get accounts database state: %v", err)
	}
	return mainState, accountsState, nil
}

func (h *CommandHandler) HandleFastSync(peeraddr string) (SyncStats, error) {
	if peeraddr == "" {
		return SyncStats{}, fmt.Errorf("usage: fastsync <peer_multiaddr>")
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

		_, syncErr = h.FastSyncer.HandleSync(addrInfo.ID)
		if syncErr == nil {
			break
		}
	}

	if syncErr != nil {
		return SyncStats{}, fmt.Errorf("sync failed after %d attempts: %v", maxRetries, syncErr)
	}

	// Get post-sync states
	newMainState, err := DB_OPs.GetDatabaseState(h.MainClient.Client)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to get main database state after sync: %v", err)
	}

	newAccountsState, err := DB_OPs.GetDatabaseState(h.DIDClient.Client)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to get accounts database state after sync: %v", err)
	}

	return SyncStats{
		TimeTaken:     time.Since(startTime),
		MainState:     newMainState,
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

	modeLower := strings.ToLower(mode)
	if modeLower != "server" && modeLower != "client" {
		return SyncStats{}, fmt.Errorf("invalid mode: %s. Must be 'server' or 'client'", mode)
	}

	fmt.Printf("Starting first sync with peer %s (mode: %s)\n", addrInfo.ID.String(), modeLower)
	startTime := time.Now().UTC()

	var syncErr error
	if modeLower == "server" {
		// Server mode: export and send all data
		fmt.Println(">>> Running in SERVER mode - exporting all data...")
		syncErr = h.FastSyncer.FirstSyncServer(addrInfo.ID)
	} else {
		// Client mode: receive and load all data
		fmt.Println(">>> Running in CLIENT mode - receiving all data...")
		syncErr = h.FastSyncer.FirstSyncClient(addrInfo.ID)
	}

	if syncErr != nil {
		return SyncStats{}, fmt.Errorf("first sync failed: %v", syncErr)
	}

	// Get post-sync states
	newMainState, err := DB_OPs.GetDatabaseState(h.MainClient.Client)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to get main database state after sync: %v", err)
	}

	newAccountsState, err := DB_OPs.GetDatabaseState(h.DIDClient.Client)
	if err != nil {
		return SyncStats{}, fmt.Errorf("failed to get accounts database state after sync: %v", err)
	}

	return SyncStats{
		TimeTaken:     time.Since(startTime),
		MainState:     newMainState,
		AccountsState: newAccountsState,
	}, nil
}

func (h *CommandHandler) HandleGetDID(did string) (*DB_OPs.Account, error) {
	if did == "" {
		return nil, fmt.Errorf("usage: getDID <did>")
	}

	doc, err := DB_OPs.GetAccountByDID(h.MainClient, did)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve DID %s: %v", did, err)
	}

	return doc, nil
}
