package FastsyncV2

// HandleCatchUpSync syncs blocks [fromBlock..remoteTip] without Merkle bisection.
//
// Use this after a bootstrap snapshot has loaded blocks [0..X]: call
// HandleCatchUpSync(ctx, X+1, targetPeer) to reconcile the remaining blocks to the
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

// Note: tryRefreshAuth is defined below but currently unused (AUTH_TTL = 48h).
// Kept for reference — re-enable the commented blocks above if TTL is reduced.

// buildDataMissingTag scans [fromBlock..remoteTip] and returns a Tag covering
// blocks that need DataSync (absent or data-incomplete per blockNeedsDataSync).
// Consecutive blocks needing DataSync are coalesced into a single RangeTag to
// minimise round-trips.
