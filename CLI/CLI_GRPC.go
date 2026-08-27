package CLI

import (
	"context"
	"fmt"
	"time"

	"strings"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/txindex"
	"gossipnode/config"
	"gossipnode/helper"
	"gossipnode/messaging/directMSG"
	"gossipnode/node"
	"gossipnode/seed"
	"gossipnode/thebesync"

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

// HandleFastSync — RETIRED (fastsync V1). V1's Merkle-hashmap sync and its
// blind latest_block/AVRO handling were superseded by FastsyncV2 — V1 was the
// last writer of the latest_block marker outside the monotonic choke point in
// DB_OPs/latest_block.go. The gRPC surface is preserved; use fastsyncv2 /
// catchup instead.
func (h *CommandHandler) HandleFastSync(peeraddr string) (SyncStats, error) {
	return SyncStats{}, fmt.Errorf("fastsync (V1) is retired — use 'fastsyncv2 <peer_multiaddr>' or 'catchup' (FastsyncV2)")
}

func (h *CommandHandler) HandleFastSyncV2(peeraddr string) (SyncStats, error) {
	return SyncStats{}, fmt.Errorf("fastsync removed: use ThebeDB sync instead")
}

func (h *CommandHandler) HandleCatchUpSync(ctx context.Context, peeraddr string, fromBlock uint64) (SyncStats, error) {
	if peeraddr == "" {
		return SyncStats{}, fmt.Errorf("usage: catchup <peer_multiaddr> [from_block]")
	}
	// ThebeSync (FastSync v4) auto-detects the range from the local tip; fromBlock
	// is accepted for wire compatibility but no longer used.
	_ = fromBlock
	if !h.PullAllowed {
		return SyncStats{}, fmt.Errorf("node is configured as a serve-only participant (pulling disabled). cannot pull data")
	}
	if h.Node == nil || h.Node.Host == nil {
		return SyncStats{}, fmt.Errorf("node host unavailable")
	}

	startTime := time.Now().UTC()
	if _, err := thebesync.CatchUp(ctx, h.Node.Host, peeraddr); err != nil {
		return SyncStats{}, fmt.Errorf("ThebeSync catchup failed: %w", err)
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
	// Retired: account state is synced as part of ThebeSync (FastSync v4) catchup.
	return SyncStats{}, fmt.Errorf("accountsync is retired — use 'catchup <peer_multiaddr>' (ThebeSync)")
}

// HandleFirstSync — RETIRED (fastsync V1 AVRO whole-DB exchange). Superseded
// by the bootstrap snapshot flow (DOCKER.md) + FastsyncV2 catchup, which sync
// incrementally with verification instead of replacing databases wholesale.
// The gRPC surface is preserved.
func (h *CommandHandler) HandleFirstSync(peeraddr string, mode string) (SyncStats, error) {
	return SyncStats{}, fmt.Errorf("firstsync (V1 AVRO exchange) is retired — bootstrap from snapshot (see DOCKER.md), then 'catchup' (FastsyncV2)")
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
