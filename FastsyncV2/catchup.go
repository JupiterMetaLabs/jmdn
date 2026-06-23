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
	authpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/availability/auth"
	ackpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/ack"
	datasyncpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/datasync"
	headersyncpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/headersync"
	phasepb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/phase"
	taggingpb "github.com/JupiterMetaLabs/JMDN-FastSync/common/proto/tagging"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types"
	"github.com/JupiterMetaLabs/JMDN-FastSync/common/types/constants"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// Note: tryRefreshAuth is defined below but currently unused (AUTH_TTL = 48h).
// Kept for reference — re-enable the commented blocks above if TTL is reduced.

// HandleCatchUpSync is the public entry point. See package-level doc above.
//
// fromBlock is the first block AFTER the guaranteed-complete bootstrap range.
// It anchors the gap scan — buildMissingTag scans [fromBlock..remoteTip] and
// fetches only what is absent locally.
//
// Lifecycle:
//
//	Stage 1 — bootstrap loads [0..X] (complete, no gaps)
//	Stage 2 — HandleCatchUpSync(X+1, peer) → syncs [X+1..T1], no gaps expected
//	Stage 3 — node offline, misses Y blocks; HandleCatchUpSync(X+1, peer) again
//	           → buildMissingTag finds any Stage-2 gaps + new [lastSynced+1..T2]
//
// fromBlock should always be bootstrapTip+1 (set in fastsync.catch_up_from_block
// config). Never use localTip+1: if Stage 2 was partial, localTip may be in the
// middle of a gap and the scan would skip missing blocks below it.
func (fs *FastsyncV2) HandleCatchUpSync(fromBlock uint64, targetPeer string) error {
	catchUpStart := time.Now()

	// fromBlock=0 is a safety fallback only — callers should always pass
	// bootstrapTip+1 (from config catch_up_from_block). Using localTip+1 here
	// would silently skip gaps below localTip if Stage 2 was interrupted.
	if fromBlock == 0 {
		fromBlock = 1
		log.Printf("[CatchUpSync] fromBlock not set, defaulting to 1 (full scan from genesis)")
	}

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

	// remoteTip is the peer's latest block number (BlockHeight field, added in add/catchup).
	// Old peers (pre-add/catchup binary) leave this as 0 — fall back to our own local tip
	// so we can at least close any gaps within our already-downloaded range.
	// New blocks beyond our local tip will be picked up once the peer is updated.
	remoteTip := availResp.BlockHeight
	if remoteTip == 0 {
		localTip := fs.blockInfoAdapter.GetBlockNumber()
		if localTip == 0 {
			return fmt.Errorf("catchup: peer %s returned block_height=0 and local tip is also 0 — peer needs the add/catchup binary update", info.ID)
		}
		log.Printf("[CatchUpSync] WARNING: peer %s returned block_height=0 (pre-add/catchup binary). "+
			"Falling back to local tip %d. Update the peer node to sync new blocks beyond local tip.",
			info.ID, localTip)
		remoteTip = localTip
	}
	if remoteTip < fromBlock {
		log.Printf("[CatchUpSync] remoteTip %d < fromBlock %d — nothing to sync", remoteTip, fromBlock)
		return nil
	}

	log.Printf("[CatchUpSync] phase 1 complete: remoteTip=%d auth=%s", remoteTip, availResp.Auth.UUID)

	remotes := []*availabilitypb.AvailabilityResponse{availResp}

	// ── Build header-missing tag (sparse gap detection) ─────────────────
	// Scan local DB for blocks already present in [fromBlock..remoteTip] and
	// compute the complement — only header-missing blocks are fetched here.
	// NOTE: PubSub announcements may have already written block headers without
	// transaction data. Those blocks appear "present" to the iterator but lack
	// NonHeaders data. Phase 3 (DataSync) always runs for the full range to fix
	// this independently of whether Phase 2 (HeaderSync) found anything to do.
	catchUpTag, err := fs.buildMissingTag(fromBlock, remoteTip)
	if err != nil {
		return fmt.Errorf("catchup: scan local blocks: %w", err)
	}

	// ── Phase 2: HeaderSync ───────────────────────────────────────────────
	log.Printf("[CatchUpSync] phase 2: header sync [%d..%d]", fromBlock, remoteTip)

	if len(catchUpTag.Range) > 0 || len(catchUpTag.BlockNumber) > 0 {
		log.Printf("[CatchUpSync] %d missing header range(s) to fetch", len(catchUpTag.Range))
		_, err = fs.HeaderRouter.HeaderSync(
			&headersyncpb.HeaderSyncRequest{Tag: catchUpTag},
			remotes,
			false, // syncConfirmation=false: skip Merkle, we know the exact range
		)
		if err != nil {
			return fmt.Errorf("catchup: header sync: %w", err)
		}
		log.Printf("[CatchUpSync] phase 2 complete")
	} else {
		log.Printf("[CatchUpSync] phase 2 skipped: all headers present in [%d..%d]", fromBlock, remoteTip)
	}

	// ── Phase 3: DataSync ─────────────────────────────────────────────────
	// Scan local blocks to find which ones are missing NonHeaders data.
	// StarkProof is written ONLY by DataSync (immudb_data_writer.go) — absent or
	// empty means the block needs DataSync regardless of whether HeaderSync ran.
	// Blocks written only by PubSub/HeaderSync will have StarkProof==nil.
	log.Printf("[CatchUpSync] phase 3: scanning for data-missing blocks [%d..%d]", fromBlock, remoteTip)

	dataMissingTag, err := fs.buildDataMissingTag(fromBlock, remoteTip)
	if err != nil {
		return fmt.Errorf("catchup: scan data-missing blocks: %w", err)
	}

	var taggedAccounts *taggingpb.TaggedAccounts
	if len(dataMissingTag.Range) == 0 && len(dataMissingTag.BlockNumber) == 0 {
		log.Printf("[CatchUpSync] phase 3 skipped: all blocks in [%d..%d] already have data", fromBlock, remoteTip)
	} else {
		log.Printf("[CatchUpSync] phase 3: %d data-missing range(s) to fetch", len(dataMissingTag.Range))
		dataSyncReq := &datasyncpb.DataSyncRequest{
			Tag:     dataMissingTag,
			Version: uint32(commsVersion),
			Ack:     &ackpb.Ack{Ok: true},
			Phase: &phasepb.Phase{
				PresentPhase:    constants.HEADER_SYNC_RESPONSE,
				SuccessivePhase: constants.DATA_SYNC_REQUEST,
				Success:         true,
				Auth:            &authpb.Auth{UUID: availResp.Auth.UUID},
			},
		}
		taggedAccounts, err = fs.DataRouter.DataSync(dataSyncReq, remotes)
		if err != nil {
			return fmt.Errorf("catchup: data sync: %w", err)
		}
		log.Printf("[CatchUpSync] phase 3 complete")
	}

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

	// Always update latest_block regardless of which phases ran.
	// This is critical when PubSub blocks were header-only before this run.
	fs.reconcileLocalLatestBlock()

	// ── Phase 8: Post-sync verification ──────────────────────────────────
	// Re-run buildDataMissingTag over the same range. If sync succeeded, the
	// returned tag will be empty (all blocks now have StarkProof set).
	// Any non-empty ranges indicate blocks that are still data-incomplete.
	log.Printf("[CatchUpSync] phase 8: verifying sync completeness [%d..%d]", fromBlock, remoteTip)

	verifyTag, verifyErr := fs.buildDataMissingTag(fromBlock, remoteTip)
	if verifyErr != nil {
		log.Printf("[CatchUpSync] phase 8 warning: verification scan failed: %v", verifyErr)
	} else if len(verifyTag.Range) == 0 && len(verifyTag.BlockNumber) == 0 {
		log.Printf("[CatchUpSync] phase 8: PASS — all blocks in [%d..%d] have data", fromBlock, remoteTip)
	} else {
		log.Printf("[CatchUpSync] phase 8: INCOMPLETE — %d range(s) still missing data:", len(verifyTag.Range))
		for _, r := range verifyTag.Range {
			log.Printf("[CatchUpSync]   missing data: blocks [%d..%d] (%d blocks)",
				r.Start, r.End, r.End-r.Start+1)
		}
		for _, bn := range verifyTag.BlockNumber {
			log.Printf("[CatchUpSync]   missing data: block %d", bn)
		}
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

// buildDataMissingTag scans [fromBlock..remoteTip] and returns a Tag covering
// blocks that need DataSync — i.e. blocks where NonHeaders (txs, ZK proof) have
// not been written yet.
//
// A block needs DataSync when:
//   - It is absent from the local DB entirely (gap in the iterator), OR
//   - It is present but StarkProof is empty. StarkProof is written ONLY by
//     DataSync (immudb_data_writer.go:59); HeaderSync and PubSub never set it.
//
// Limitation: blocks with a genuinely empty ZK proof will always have
// len(StarkProof)==0 even after DataSync. They will be re-fetched on every
// catchup run. This is safe (DataSync is idempotent) and rare in practice on a
// ZK L2 where every finalized block carries a proof.
//
// Consecutive blocks needing DataSync are coalesced into a single RangeTag to
// minimise round-trips.

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

func (fs *FastsyncV2) buildDataMissingTag(fromBlock, remoteTip uint64) (*taggingpb.Tag, error) {
	iter := fs.blockInfoAdapter.NewBlockIterator(fromBlock, remoteTip, catchUpBatchSize)
	defer iter.Close()

	var ranges []*taggingpb.RangeTag
	cursor := fromBlock
	runStart := uint64(0)
	inRun := false

	// Start a new run at b (or extend if already in one).
	addToRun := func(b uint64) {
		if !inRun {
			runStart = b
			inRun = true
		}
	}
	// Close the active run, capping it at end.
	endRunAt := func(end uint64) {
		if inRun {
			ranges = append(ranges, &taggingpb.RangeTag{Start: runStart, End: end})
			inRun = false
		}
	}

	for {
		batch, err := iter.Next()
		if err != nil {
			return nil, fmt.Errorf("data-missing block iterator: %w", err)
		}
		if len(batch) == 0 {
			// Remaining [cursor..remoteTip] are absent — include them.
			if cursor <= remoteTip {
				addToRun(cursor)
				endRunAt(remoteTip)
			}
			break
		}

		for _, blk := range batch {
			b := blk.BlockNumber
			if b < cursor {
				continue // shouldn't happen with a sorted iterator
			}

			// Absent blocks [cursor..b-1]: they need DataSync — extend or start run.
			if b > cursor {
				addToRun(cursor)
				// Run is now active through at least b-1.
				// We decide below whether b also extends it or closes it.
			}

			if len(blk.StarkProof) == 0 {
				// Block b is present but data-incomplete — keep the run going.
				addToRun(b)
			} else {
				// Block b is complete — close any active run just before b.
				if inRun {
					endRunAt(b - 1)
				}
			}

			cursor = b + 1
		}
	}

	return &taggingpb.Tag{Range: ranges}, nil
}
