package CLI

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/cassata"
	"gossipnode/DB_OPs/thebestatus"
	"gossipnode/config"
	"gossipnode/helper"
	"gossipnode/messaging/directMSG"
	"gossipnode/node"
	"gossipnode/seed"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/peer"
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
	MainState     *thebestatus.Status
	AccountsState *thebestatus.Status
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

func (h *CommandHandler) CheckDBStats() (*thebestatus.Status, *thebestatus.Status, error) {
	// Status now comes from ThebeDB chain/projection heads.
	mainState, err := h.currentThebeStatus()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get main database state: %v", err)
	}

	// JMDN no longer uses a separate accountsdb immutable state model in Thebe mode.
	accountsState := mainState
	return mainState, accountsState, nil
}

func (h *CommandHandler) HandleFastSyncV2(peeraddr string) (SyncStats, error) {
	if peeraddr == "" {
		return SyncStats{}, fmt.Errorf("usage: fastsyncv2 <peer_multiaddr>")
	}

	// Make sure engine exists
	if h.FastSyncerV2 == nil {
		return SyncStats{}, fmt.Errorf("FastsyncV2 engine is inactive")
	}

	startTime := time.Now().UTC()
	err := h.FastSyncerV2.HandleSync(peeraddr)
	if err != nil {
		return SyncStats{}, fmt.Errorf("FastsyncV2 failed: %w", err)
	}

	// Re-fetch DB states to report. FastsyncV2 doesn't require MainClient/DIDClient
	// for the sync itself, so guard against nil before querying.
	var newMainState, newAccountsState *thebestatus.Status
	newMainState, _ = h.currentThebeStatus()
	newAccountsState = newMainState

	return SyncStats{
		TimeTaken:     time.Since(startTime),
		MainState:     newMainState,
		AccountsState: newAccountsState,
	}, nil
}

func (h *CommandHandler) currentThebeStatus() (*thebestatus.Status, error) {
	st, err := thebestatus.StatusFromCurrentDB(context.Background())
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (h *CommandHandler) HandleGetDID(did string) (*DB_OPs.Account, error) {
	if did == "" {
		return nil, fmt.Errorf("usage: getDID <did>")
	}

	if h.Cassata != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		acc, err := h.Cassata.GetAccountByDID(ctx, did)
		if err == nil {
			return cassataAccountToDomain(acc)
		}
	}

	doc, err := DB_OPs.GetAccountByDID(h.MainClient, did)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve DID %s: %v", did, err)
	}

	return doc, nil
}

func cassataAccountToDomain(a *cassata.AccountResult) (*DB_OPs.Account, error) {
	if a == nil {
		return nil, fmt.Errorf("nil account result")
	}

	nonce, err := strconv.ParseUint(a.Nonce, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse nonce: %w", err)
	}

	metadata := map[string]interface{}{}
	if len(a.Metadata) > 0 {
		if err := json.Unmarshal(a.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("decode metadata: %w", err)
		}
	}

	did := ""
	if a.DIDAddress != nil {
		did = *a.DIDAddress
	}

	accountType := "publickey"
	if a.AccountType != 0 {
		accountType = "contract"
	}

	return &DB_OPs.Account{
		Address:     common.HexToAddress(a.Address),
		DIDAddress:  did,
		Balance:     a.BalanceWei,
		Nonce:       nonce,
		AccountType: accountType,
		Metadata:    metadata,
		CreatedAt:   a.CreatedAt.Unix(),
		UpdatedAt:   a.UpdatedAt.Unix(),
	}, nil
}
