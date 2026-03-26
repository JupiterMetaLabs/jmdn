package FastsyncV2

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"

	"gossipnode/DB_OPs/Nodeinfo"

	"github.com/JupiterMetaLabs/JMDN-FastSync/common/WAL"
	availabilitypb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/availability"
	headersyncpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/headersync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	wal_types "github.com/JupiterMetaLabs/JMDN-FastSync/common/types/wal"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/availability"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/datasync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/headersync"
	pots "github.com/JupiterMetaLabs/JMDN-FastSync/core/pots"
	potsrequesthelper "github.com/JupiterMetaLabs/JMDN-FastSync/core/pots/helper"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/priorsync"
	"github.com/JupiterMetaLabs/JMDN-FastSync/core/reconsillation"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const (
	protocolVersion = 1
	checksumVersion = 1
	commsVersion    = 2
)

type FastsyncV2 struct {
	Host             host.Host
	NodeInfo         *types.Nodeinfo
	WAL              *WAL.WAL
	PoTSWAL          *WAL.WAL
	PriorRouter      priorsync.Priorsync_router
	HeaderRouter     headersync.Headersync_router
	DataRouter       datasync.DataSync_router
	AvailRouter      availability.Availability_router
	ReconRouter      reconsillation.Reconciliation_router
	PoTSRouter       *pots.PoTS
	blockInfoAdapter types.BlockInfo
}

// NewFastsyncV2 initializes the JMDN-FastSync engine over the libp2p host.
func NewFastsyncV2(h host.Host) (*FastsyncV2, error) {
	ctx := context.Background()

	// 1. Initialize NodeInfo using the ImmuDB wrapper
	blockInfo := NodeInfo.NewSyncStruct()

	nodeinfo := &types.Nodeinfo{
		PeerID:    h.ID(),
		Multiaddr: h.Addrs(),
		Version:   commsVersion,
		BlockInfo: blockInfo,
	}

	// 2. Setup standard WAL
	walDir := wal_types.DefaultDir
	if err := os.MkdirAll(walDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create WAL directory: %w", err)
	}
	wal, err := WAL.NewWAL(walDir, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize main WAL: %w", err)
	}

	// 3. Setup PoTS WAL
	potsWALDir := filepath.Join(wal_types.DefaultDir, "..", "internal", "PoTS")
	if err := os.MkdirAll(potsWALDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create PoTS WAL directory: %w", err)
	}
	potsWAL, err := WAL.NewWAL(potsWALDir, 100)
	if err != nil {
		return nil, fmt.Errorf("failed to init PoTS WAL: %w", err)
	}

	// 4. Initialize Routers
	priorRouter := priorsync.NewPriorSyncRouter()
	headerRouter := headersync.NewHeaderSync()
	dataRouter := datasync.NewDataSync()
	availRouter := availability.NewAvailability()
	reconRouter := reconsillation.NewReconciliation()
	potsRouter := pots.NewPoTS()

	// Set generic SyncVars
	priorRouter.SetSyncVars(ctx, protocolVersion, checksumVersion, *nodeinfo, h, wal)
	headerRouter.SetSyncVars(ctx, protocolVersion, *nodeinfo, h, wal)
	dataRouter.SetSyncVars(ctx, protocolVersion, *nodeinfo, h, wal)
	
	syncVars := priorRouter.GetSyncVars()
	availRouter.SetSyncVarsConfig(ctx, *syncVars)
	reconRouter.SetSyncVarsConfig(ctx, *syncVars)

	// Set PoTS parameters
	potsRouter.SetSyncVars(ctx, protocolVersion, *nodeinfo, h)
	potsRouter.SetWAL(ctx, potsWAL)

	// 5. Wire Network Handlers for Server features
	availability.FastsyncReady().IAmAvailable()
	
	go func() {
		log.Printf("FastsyncV2 starting PriorSync handler...")
		if err := priorRouter.SetupNetworkHandlers(true); err != nil && err != context.Canceled {
			log.Printf("FastsyncV2 HandlePriorSync error: %v", err)
		}
	}()

	return &FastsyncV2{
		Host:             h,
		NodeInfo:         nodeinfo,
		WAL:              wal,
		PoTSWAL:          potsWAL,
		PriorRouter:      priorRouter,
		HeaderRouter:     headerRouter,
		DataRouter:       dataRouter,
		AvailRouter:      availRouter,
		ReconRouter:      reconRouter,
		PoTSRouter:       potsRouter,
		blockInfoAdapter: blockInfo,
	}, nil
}

// HandleSync executes the step-by-step FastSync flow matching the testsuite.
func (fs *FastsyncV2) HandleSync(targetPeer string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Parse target peer multiaddress
	maddr, err := multiaddr.NewMultiaddr(targetPeer)
	if err != nil {
		return fmt.Errorf("failed to parse multiaddr: %w", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("failed to get addr info: %w", err)
	}

	// Connect to peer (if not connected already)
	if err := fs.Host.Connect(ctx, *info); err != nil {
		return fmt.Errorf("failed to connect to host: %w", err)
	}

	// PHASE 1: Availability Check
	log.Printf("[FastsyncV2] Sending Availability Request to %s", info.ID)
	availResp, err := fs.AvailRouter.SendAvailabilityRequest(ctx, fs.PriorRouter.GetSyncVars(), *fs.NodeInfo, 0, math.MaxUint64)
	if err != nil || !availResp.IsAvailable || availResp.Auth == nil || availResp.Auth.UUID == "" {
		return fmt.Errorf("target node unavailable or lack auth: %v", err)
	}
	log.Printf("[FastsyncV2] Authorized with UUID: %s", availResp.Auth.UUID)

	// PHASE 2: PriorSync
	localBlocknumber := fs.blockInfoAdapter.GetBlockDetails().Blocknumber
	log.Printf("[FastsyncV2] Sending PriorSync. Local block: %d", localBlocknumber)
	resp, err := fs.PriorRouter.PriorSync(0, localBlocknumber, 0, math.MaxUint64, fs.NodeInfo, availResp.Auth)
	if err != nil {
		return fmt.Errorf("priorsync failed: %w", err)
	}

	if resp.Headersync == nil || resp.Headersync.Tag == nil {
		log.Println("[FastsyncV2] DB checksums match. No further sync needed.")
		return nil
	}

	// PHASE 3: Header Sync
	log.Println("[FastsyncV2] Initiating HeaderSync...")
	datasyncReq, err := fs.HeaderRouter.HeaderSync(resp.Headersync, []*availabilitypb.AvailabilityResponse{availResp}, true)
	if err != nil && datasyncReq != nil {
		return fmt.Errorf("header sync failed: %w", err)
	}

	// PHASE 4: Data Sync
	log.Println("[FastsyncV2] Initiating DataSync...")
	taggedAccounts, err := fs.DataRouter.DataSync(datasyncReq, []*availabilitypb.AvailabilityResponse{availResp})
	if err != nil {
		return fmt.Errorf("data sync failed: %w", err)
	}

	// PHASE 5: Reconciliation
	log.Println("[FastsyncV2] Initiating Reconciliation...")
	reconciledCount, failed, err := fs.ReconRouter.Reconcile(taggedAccounts)
	if err != nil {
		log.Printf("[FastsyncV2] Reconciliation warnings: %v", err)
	}
	log.Printf("[FastsyncV2] Reconciled %d accounts. Failed: %d", reconciledCount, len(failed))

	// PHASE 6: PoTS Catch-Up
	log.Println("[FastsyncV2] Executing PoTS Continuous Verification...")
	latestSyncedBlock := fs.blockInfoAdapter.GetBlockDetails().Blocknumber
	potsWALIface, err := fs.PoTSRouter.GetWAL()
	if err != nil {
		return fmt.Errorf("failed to access PoTS WAL: %w", err)
	}
	walCount, _ := potsWALIface.Count(ctx)
	walBlocks, _ := potsWALIface.Read(ctx, 0, int(walCount))

	potsBlocksMap := make(map[uint64][]byte, len(walBlocks))
	for _, blk := range walBlocks {
		potsBlocksMap[blk.BlockNumber] = blk.BlockHash.Bytes()
	}

	potsReq := potsrequesthelper.NewPoTSRequestBuilder().
		AddMap(potsBlocksMap).
		AddLatestBlock(latestSyncedBlock).
		AddAuth(availResp.Auth).
		Build()

	potsResp, err := fs.PoTSRouter.SendPoTSRequest(ctx, potsReq, *fs.NodeInfo)
	if err != nil {
		return fmt.Errorf("PoTS sync request failed: %w", err)
	}

	if potsResp.Tag != nil && (len(potsResp.Tag.Range) > 0 || len(potsResp.Tag.BlockNumber) > 0) {
		log.Println("[FastsyncV2] PoTS identified missing overlaps. Second round triggered.")
		potsDatasyncReq, err := fs.HeaderRouter.HeaderSync(&headersyncpb.HeaderSyncRequest{Tag: potsResp.Tag}, []*availabilitypb.AvailabilityResponse{availResp}, false)
		if err == nil {
			potsTaggedAccts, _ := fs.DataRouter.DataSync(potsDatasyncReq, []*availabilitypb.AvailabilityResponse{availResp})
			fs.ReconRouter.Reconcile(potsTaggedAccts)
		}
	}

	log.Println("[FastsyncV2] Completely Synchronized!")
	return nil
}

// Close ensures all databases and WAL are properly flushed.
func (fs *FastsyncV2) Close() {
	if fs.WAL != nil {
		fs.WAL.Close()
	}
	if fs.PoTSWAL != nil {
		fs.PoTSWAL.Close()
	}
	fs.PoTSRouter.Close()
	fs.PriorRouter.Close()
	fs.ReconRouter.Close()
}
