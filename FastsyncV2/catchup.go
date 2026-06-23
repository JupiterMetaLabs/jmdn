package FastsyncV2

// HandleCatchUpSync syncs blocks [fromBlock..remoteTip] without Merkle bisection.
//
// Use this after a bootstrap snapshot has loaded blocks [0..X]: call
// HandleCatchUpSync(X+1, targetPeer) to reconcile the remaining blocks to the
// current chain tip.
//
// Unlike HandleSync / HandleStartupSync, this path skips PriorSync entirely.
// It builds the missing range directly from the availability response:
//
//	Phase 1 — Availability    → get auth token, discover remoteTip
//	Phase 2 — HeaderSync      → fetch headers [fromBlock..remoteTip] (no Merkle confirmation)
//	Phase 3 — DataSync        → fetch block bodies
//	Phase 4 — AccountSync     → sync zero-tx accounts not covered by DataSync
//	Phase 5 — Reconciliation  → replay txs, commit account balances
//	Phase 6 — Re-auth         → refresh expired token before PoTS
//	Phase 7 — PoTS            → fetch blocks produced while phases 2-5 ran
//
// targetPeer must be a libp2p multiaddr with an embedded peer ID, e.g.:
//
//	/ip4/192.168.1.5/tcp/15000/p2p/12D3KooW...
import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	availabilitypb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/availability"
	headersyncpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/headersync"
	taggingpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/tagging"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// Note: tryRefreshAuth is defined below but currently unused (AUTH_TTL = 48h).
// Kept for reference — re-enable the commented blocks above if TTL is reduced.

// HandleCatchUpSync is the public entry point. See package-level doc above.
func (fs *FastsyncV2) HandleCatchUpSync(fromBlock uint64, targetPeer string) error {
	catchUpStart := time.Now()

	// Use a generous timeout — catching up on days of blocks takes much longer
	// than a normal incremental sync. Callers can wrap in their own deadline if needed.
	ctx, cancel := context.WithTimeout(context.Background(), fs.syncTimeout)
	defer cancel()

	// ── Parse and connect ─────────────────────────────────────────────────
	maddr, err := multiaddr.NewMultiaddr(targetPeer)
	if err != nil {
		return fmt.Errorf("catchup: invalid multiaddr %q: %w", targetPeer, err)
	}
	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("catchup: extract peer info: %w", err)
	}
	if err := fs.Host.Connect(ctx, *info); err != nil {
		return fmt.Errorf("catchup: connect to peer %s: %w", info.ID, err)
	}

	peerAddrs := fs.Host.Peerstore().Addrs(info.ID)
	if len(peerAddrs) == 0 {
		peerAddrs = info.Addrs
	}
	targetNodeInfo := &types.Nodeinfo{
		PeerID:    info.ID,
		Multiaddr: peerAddrs,
		Version:   commsVersion,
	}

	log.Printf("[CatchUpSync] starting from block %d → peer %s", fromBlock, info.ID)

	// ── Phase 1: Availability ─────────────────────────────────────────────
	log.Printf("[CatchUpSync] phase 1: availability probe")

	availResp, err := fs.AvailRouter.SendAvailabilityRequest(
		ctx, fs.PriorRouter.GetSyncVars(), *targetNodeInfo, fromBlock, math.MaxUint64,
	)
	if err != nil {
		return fmt.Errorf("catchup: availability: %w", err)
	}
	if !availResp.IsAvailable {
		return fmt.Errorf("catchup: peer %s not available", info.ID)
	}
	if availResp.Auth == nil || availResp.Auth.UUID == "" {
		return fmt.Errorf("catchup: peer %s returned no auth token", info.ID)
	}

	// remoteTip is the highest block the peer reported.
	remoteTip := uint64(availResp.BlockMerge)
	if remoteTip < fromBlock {
		return fmt.Errorf("catchup: remoteTip %d < fromBlock %d — nothing to sync", remoteTip, fromBlock)
	}

	log.Printf("[CatchUpSync] phase 1 complete: remoteTip=%d auth=%s", remoteTip, availResp.Auth.UUID)

	remotes := []*availabilitypb.AvailabilityResponse{availResp}

	// ── Build the catch-up tag (sparse gap detection) ────────────────────
	// Scan local DB for blocks already present in [fromBlock..remoteTip] and
	// compute the complement — only the gaps are fetched, not the full range.
	catchUpTag, err := fs.buildMissingTag(fromBlock, remoteTip)
	if err != nil {
		return fmt.Errorf("catchup: scan local blocks: %w", err)
	}
	if len(catchUpTag.Range) == 0 && len(catchUpTag.BlockNumber) == 0 {
		log.Printf("[CatchUpSync] all blocks [%d..%d] already present locally", fromBlock, remoteTip)
		return nil
	}
	log.Printf("[CatchUpSync] %d missing range(s) to fetch", len(catchUpTag.Range))

	// ── Phase 2: HeaderSync ───────────────────────────────────────────────
	log.Printf("[CatchUpSync] phase 2: header sync [%d..%d]", fromBlock, remoteTip)

	dataSyncReq, err := fs.HeaderRouter.HeaderSync(
		&headersyncpb.HeaderSyncRequest{Tag: catchUpTag},
		remotes,
		false, // syncConfirmation=false: skip Merkle, we know the exact range
	)
	if err != nil {
		return fmt.Errorf("catchup: header sync: %w", err)
	}
	log.Printf("[CatchUpSync] phase 2 complete")

	// ── Phase 3: DataSync ─────────────────────────────────────────────────
	log.Printf("[CatchUpSync] phase 3: data sync")

	// dataSyncReq is nil if HeaderSync found no blocks to write (range already
	// present locally). Skip DataSync in that case — same behaviour as HandleSync.
	if dataSyncReq == nil {
		log.Printf("[CatchUpSync] phase 3 skipped: no DataSync request from HeaderSync")
		return nil
	}

	taggedAccounts, err := fs.DataRouter.DataSync(dataSyncReq, remotes)
	if err != nil {
		return fmt.Errorf("catchup: data sync: %w", err)
	}
	log.Printf("[CatchUpSync] phase 3 complete")

	// Refresh the local block marker after writing a large batch of data.
	fs.reconcileLocalLatestBlock()

	// ── Phase 3.5: FetchAccounts — pull tagged accounts missing locally ───
	if taggedAccounts != nil && len(taggedAccounts.Accounts) > 0 {
		// AUTH_TTL is now 48h so no re-auth needed here.
		// if refreshed, ok := fs.tryRefreshAuth(ctx, targetNodeInfo, fromBlock); ok {
		// 	availResp = refreshed
		// 	remotes = []*availabilitypb.AvailabilityResponse{availResp}
		// }

		missingMap := make(map[string]bool)
		accountMgr := fs.blockInfoAdapter.NewAccountManager()
		for addr := range taggedAccounts.Accounts {
			acc, err := accountMgr.GetAccountByAddress(addr)
			if err == nil && acc == nil {
				missingMap[addr] = true
			}
		}
		if len(missingMap) > 0 {
			log.Printf("[CatchUpSync] phase 3.5: fetching %d missing tagged accounts", len(missingMap))
			resp, err := fs.AccountSyncRouter.FetchAccounts(availResp, missingMap)
			if err != nil {
				log.Printf("[CatchUpSync] phase 3.5 warning: FetchAccounts failed: %v", err)
			} else if resp != nil && len(resp.GetAccounts()) > 0 {
				accounts := protoAccountsToTypes(resp.GetAccounts())
				if writeErr := accountMgr.WriteAccounts(accounts); writeErr != nil {
					log.Printf("[CatchUpSync] phase 3.5 warning: WriteAccounts failed: %v", writeErr)
				} else {
					log.Printf("[CatchUpSync] phase 3.5 complete: wrote %d accounts", len(accounts))
				}
			}
		}
	}

	// ── Phase 4: AccountSync ──────────────────────────────────────────────
	// Syncs zero-tx accounts not covered by DataSync TaggedAccounts.
	log.Printf("[CatchUpSync] phase 4: account sync")

	totalMissing, err := fs.AccountSyncRouter.AccountSync(availResp)
	if err != nil {
		log.Printf("[CatchUpSync] phase 4 warning: account sync failed: %v", err)
	} else {
		log.Printf("[CatchUpSync] phase 4 complete: %d accounts synced", totalMissing)
	}

	// ── Phase 5: Reconciliation ───────────────────────────────────────────
	log.Printf("[CatchUpSync] phase 5: reconciliation")

	reconCount, failedAccounts, err := fs.ReconRouter.Reconcile(taggedAccounts, availResp)
	if err != nil {
		log.Printf("[CatchUpSync] phase 5 warning: %v", err)
	}
	log.Printf("[CatchUpSync] phase 5 complete: %d committed, %d failed", reconCount, len(failedAccounts))

	// ── Phase 6: Re-auth before PoTS (disabled — AUTH_TTL is now 48h) ─────
	// if refreshed, ok := fs.tryRefreshAuth(ctx, targetNodeInfo, 0); ok {
	// 	availResp = refreshed
	// 	remotes = []*availabilitypb.AvailabilityResponse{availResp}
	// 	log.Printf("[CatchUpSync] phase 6: re-auth ok (UUID=%s)", availResp.Auth.UUID)
	// } else {
	// 	log.Printf("[CatchUpSync] phase 6: re-auth failed — proceeding with stale token")
	// }

	// ── Phase 7: PoTS ─────────────────────────────────────────────────────
	log.Printf("[CatchUpSync] phase 7: PoTS gap fill")

	if err := fs.executePoTS(ctx, targetNodeInfo, remotes, availResp); err != nil {
		log.Printf("[CatchUpSync] phase 7 warning: PoTS failed: %v", err)
	} else {
		log.Printf("[CatchUpSync] phase 7 complete")
	}

	log.Printf("[CatchUpSync] done in %s", time.Since(catchUpStart).Round(time.Millisecond))
	return nil
}

// tryRefreshAuth sends a fresh availability request and returns the new response
// if the peer is still available and returns a valid token.
func (fs *FastsyncV2) tryRefreshAuth(ctx context.Context, targetNodeInfo *types.Nodeinfo, startBlock uint64) (*availabilitypb.AvailabilityResponse, bool) {
	resp, err := fs.AvailRouter.SendAvailabilityRequest(
		ctx, fs.PriorRouter.GetSyncVars(), *targetNodeInfo, startBlock, math.MaxUint64,
	)
	if err != nil {
		log.Printf("[CatchUpSync] auth refresh failed: %v", err)
		return nil, false
	}
	if !resp.IsAvailable || resp.Auth == nil || resp.Auth.UUID == "" {
		return nil, false
	}
	return resp, true
}

// buildMissingTag scans the local DB over [fromBlock..remoteTip] and returns a
// Tag containing only the ranges absent locally.
//
// Algorithm — O(n) time, O(batch) space:
//
//  1. Iterate local blocks in ascending order via BlockIterator.
//  2. Keep a "cursor" at the next expected block number, starting at fromBlock.
//  3. For each present block B:
//     - If cursor < B → gap [cursor..B-1] is missing → emit RangeTag.
//     - Advance cursor to B+1.
//  4. After iteration: if cursor ≤ remoteTip, emit the trailing gap.
//
// This produces the minimal set of contiguous ranges to request from the peer.
// Example: present={0,1,3,7,9,10}, fromBlock=0, remoteTip=10
//
//	→ gaps: [2..2], [4..6], [8..8]
const catchUpBatchSize = 500

func (fs *FastsyncV2) buildMissingTag(fromBlock, remoteTip uint64) (*taggingpb.Tag, error) {
	iter := fs.blockInfoAdapter.NewBlockIterator(fromBlock, remoteTip, catchUpBatchSize)
	defer iter.Close()

	var ranges []*taggingpb.RangeTag
	cursor := fromBlock

	for {
		batch, err := iter.Next()
		if err != nil {
			return nil, fmt.Errorf("block iterator: %w", err)
		}
		if len(batch) == 0 {
			break // end of iteration
		}

		for _, blk := range batch {
			b := blk.BlockNumber
			if b < cursor {
				continue // already accounted for (shouldn't happen with sorted iterator)
			}
			if b > cursor {
				// Gap: [cursor .. b-1] is missing
				ranges = append(ranges, &taggingpb.RangeTag{Start: cursor, End: b - 1})
			}
			cursor = b + 1
		}
	}

	// Trailing gap: blocks after the last present one up to remoteTip
	if cursor <= remoteTip {
		ranges = append(ranges, &taggingpb.RangeTag{Start: cursor, End: remoteTip})
	}

	return &taggingpb.Tag{Range: ranges}, nil
}
