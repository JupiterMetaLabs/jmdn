package messaging

// Legacy follow mode (consensus.legacy_follow): during the migration from the
// main branch to v3base, a v3base node seeded from a mainnet export keeps
// applying the blocks of the mainnet sequencer that still runs main.
//
// Main's blocks carry no ConsensusHash, no v3 committee certificate, no
// committee anchor, and fee recipients computed from main's certificates, so
// the v3 receive and sync gates reject them. For a LEGACY block (at or below
// legacy_follow.until_height; every block while it is 0) this file replaces
// those gates with:
//
//   - source: the block must come from the pinned main sequencer's peer ID
//     (libp2p-authenticated stream peer, gossipsub publisher under StrictSign,
//     or the ThebeSync peer a catch-up fetched it from);
//   - body: every transaction signature for this chain, tx hash over
//     contents, non-negative values, in-block nonce order, and BlockHash and
//     TxnsRoot over the transactions (always, whatever EnforceBodyBinding);
//   - linkage: parent hash and height + 1 (live path; the sync path checks
//     linkage itself);
//   - equivocation: one block per height, keyed on BlockHash (legacy blocks
//     have no ConsensusHash).
//
// The block's own FeeRecipients are applied as main applied them. Everything
// else (ConsensusHash, committee anchor, v3 certificate, fee-recipient
// recomputation, entropy effects) does not apply to legacy blocks. Blocks above
// the window follow the normal v3base rules unchanged. The trust root is the
// main sequencer, the same root main nodes trust for block contents.
//
// TEMPORARY: remove this file and its call sites after the cutover, once every
// node runs v3base.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/config/GRO"
	"gossipnode/config/settings"
	"gossipnode/helper"
	"gossipnode/messaging/BlockProcessing"
	"gossipnode/metrics"
)

// legacyFollowSettingsFn returns the legacy follow settings; a seam for tests.
var legacyFollowSettingsFn = func() settings.LegacyFollowSettings {
	if !settings.IsLoaded() {
		return settings.LegacyFollowSettings{}
	}
	return settings.Get().Consensus.LegacyFollow
}

// OverrideLegacyFollowSettings replaces the settings source and returns a
// restore func. For tests in other packages; production reads settings.
func OverrideLegacyFollowSettings(s settings.LegacyFollowSettings) (restore func()) {
	prev := legacyFollowSettingsFn
	legacyFollowSettingsFn = func() settings.LegacyFollowSettings { return s }
	return func() { legacyFollowSettingsFn = prev }
}

// LegacyFollowEnabled reports whether legacy follow mode is on.
func LegacyFollowEnabled() bool { return legacyFollowSettingsFn().Enabled }

// IsLegacyBlock reports whether b is handled by the legacy-follow rules.
func IsLegacyBlock(b *config.ZKBlock) bool {
	if b == nil {
		return false
	}
	s := legacyFollowSettingsFn()
	if !s.Enabled {
		return false
	}
	return s.UntilHeight == 0 || b.BlockNumber <= s.UntilHeight
}

// ParseLegacyMainSequencer returns the peer ID and address of
// legacy_follow.main_sequencer, which must be a multiaddr ending in /p2p/<id>.
func ParseLegacyMainSequencer(addr string) (*peer.AddrInfo, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, errors.New("consensus.legacy_follow.main_sequencer is empty")
	}
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return nil, fmt.Errorf("consensus.legacy_follow.main_sequencer %q: %w", addr, err)
	}
	info, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return nil, fmt.Errorf("consensus.legacy_follow.main_sequencer %q must end in /p2p/<peer id>: %w", addr, err)
	}
	return info, nil
}

// AuthorizeLegacySource reports whether peerID (an authenticated libp2p peer
// ID string; a "gossip:" transport tag is stripped) is the pinned main
// sequencer. Fails closed when the pin is missing or malformed.
func AuthorizeLegacySource(peerID string) bool {
	info, err := ParseLegacyMainSequencer(legacyFollowSettingsFn().MainSequencer)
	if err != nil {
		return false
	}
	return strings.TrimPrefix(strings.TrimSpace(peerID), "gossip:") == info.ID.String()
}

// legacyCatchUpNudge wakes the legacy catch-up loop when the live path sees a
// height gap. Set by the loop at start.
var (
	legacyNudgeMu      sync.Mutex
	legacyCatchUpNudge func()
)

// SetLegacyCatchUpNudge wires the legacy catch-up loop's wake-up.
func SetLegacyCatchUpNudge(fn func()) {
	legacyNudgeMu.Lock()
	legacyCatchUpNudge = fn
	legacyNudgeMu.Unlock()
}

func nudgeLegacyCatchUp() {
	legacyNudgeMu.Lock()
	fn := legacyCatchUpNudge
	legacyNudgeMu.Unlock()
	if fn != nil {
		fn()
	}
}

// legacyLinkageFn is checkLinkage for legacy blocks; a seam for tests (the
// live linkage check reads the store).
var legacyLinkageFn = checkLinkage

// validateLegacyBody runs the body checks every legacy block must pass.
func validateLegacyBody(ctx context.Context, b *config.ZKBlock) *blockRejection {
	if b == nil {
		return reject("nil_block", "block is nil")
	}
	if len(b.Transactions) > 0 && (b.CoinbaseAddr == nil || b.ZKVMAddr == nil) {
		return reject("missing_fee_addresses", "legacy block %d has transactions but no coinbase or zkVM address", b.BlockNumber)
	}
	// Main has no contract execution in block apply and cannot apply a
	// transaction without a recipient; v3base would deploy it through the EVM.
	// Refuse it so no contract state (and so no EVM-routed call) can appear
	// inside the window.
	for i := range b.Transactions {
		if b.Transactions[i].To == nil {
			return reject("legacy_contract_creation", "legacy block %d tx %s has no recipient (contract creation is not applied by main)",
				b.BlockNumber, b.Transactions[i].Hash.Hex())
		}
	}
	fillTypedTxChainID(b)
	if rej := validateBlockBody(ctx, b); rej != nil {
		return rej
	}
	// Body binding is unconditional for legacy blocks: without a certificate,
	// it is what ties BlockHash to the transactions.
	if !EnforceBodyBinding {
		if rej := checkBodyBinding(b); rej != nil {
			return rej
		}
		if ok, err := Security.CheckBlockHash(b); err != nil || !ok {
			return reject("block_hash_mismatch", "legacy block %s hash does not match tx contents: %v", b.BlockHash.Hex(), err)
		}
	}
	return nil
}

// validateLegacyBlock is the live-path gate for a legacy block whose source
// has already been authorized: body, linkage, then the equivocation record.
func validateLegacyBlock(ctx context.Context, b *config.ZKBlock) *blockRejection {
	if rej := validateLegacyBody(ctx, b); rej != nil {
		return rej
	}
	if EnforceBlockLinkage {
		if rej := legacyLinkageFn(ctx, b); rej != nil {
			if rej.reason == "height_gap" || rej.reason == "not_bootstrapped" {
				nudgeLegacyCatchUp()
			}
			return rej
		}
	}
	return checkEquivocation(b.BlockNumber, b.BlockHash.Hex())
}

// ValidateLegacySyncedBlock is the sync-path gate for a legacy block fetched
// from the authorized main sequencer; the caller checks linkage. Body checks,
// then the equivocation record.
func ValidateLegacySyncedBlock(ctx context.Context, b *config.ZKBlock) error {
	if rej := validateLegacyBody(ctx, b); rej != nil {
		return rej.err
	}
	if rej := checkEquivocation(b.BlockNumber, b.BlockHash.Hex()); rej != nil {
		return rej.err
	}
	return nil
}

// legacyBlockDedupID is the dedup key for a legacy block (no ConsensusHash).
func legacyBlockDedupID(b *config.ZKBlock) string {
	return "zkblock-legacy:" + b.BlockHash.Hex()
}

// handleLegacyBlockMessage is HandleReceivedBlockMessage for a legacy block:
// authorize the source, dedup, run the legacy gate, then apply and store
// through the same path as any block. Legacy blocks are not re-forwarded:
// every following node receives them from the main sequencer itself.
func handleLegacyBlockMessage(msg config.BlockMessage, remotePeer string) {
	b := msg.Block
	if status, ok := msg.Data["status"]; ok && status == "rejected" {
		return
	}
	if !AuthorizeLegacySource(remotePeer) {
		metrics.BlocksRejectedCounter.WithLabelValues("legacy_source_not_main_sequencer", "local").Inc()
		broadcastLogger().Warn(context.Background(), "legacy follow: dropping legacy block not sent by the pinned main sequencer",
			ion.String("from", remotePeer), ion.Uint64("block_number", b.BlockNumber))
		return
	}
	id := legacyBlockDedupID(b)
	if isMessageProcessed(id) {
		return
	}
	if rej := validateLegacyBlock(context.Background(), b); rej != nil {
		metrics.BlocksRejectedCounter.WithLabelValues("legacy_"+rej.reason, "local").Inc()
		broadcastLogger().Warn(context.Background(), "legacy follow: rejecting legacy block",
			ion.Err(rej.err), ion.String("reason", rej.reason),
			ion.Uint64("block_number", b.BlockNumber), ion.String("block_hash", b.BlockHash.Hex()))
		return
	}
	markMessageProcessed(id)

	// No contract pull before apply: no contract state can exist in the legacy
	// window (validateLegacyBody), and a peer that never answers must not hold
	// up a block (see prepareReceivedBlock).
	BlockPropagationLocalGRO.Go(GRO.BlockPropagationProcessAndValidateThread, func(ctx context.Context) error {
		if err := BlockProcessing.ProcessBlockTransactionsAndStore(context.Background(), b, nil, func() error {
			return DB_OPs.StoreZKBlock(nil, b)
		}); err != nil {
			broadcastLogger().Error(ctx, "legacy follow: block apply+store failed - block not applied (rolled back)", err,
				ion.Uint64("block_number", b.BlockNumber), ion.String("block_hash", b.BlockHash.Hex()))
			return fmt.Errorf("legacy block apply+store failed: %w", err)
		}
		if _, _, err := DB_OPs.UpdateLatestBlockMonotonic(b.BlockNumber); err != nil {
			broadcastLogger().Warn(context.Background(), "latest_block monotonic update failed (non-fatal: ReconcileBlockNumber heals forward)",
				ion.String("error", err.Error()), ion.Uint64("block_number", b.BlockNumber))
		}
		NoteHeightCommitted(b.BlockNumber)
		NoteCommittedForCheckpoint(b)
		broadcastLogger().Info(ctx, "legacy follow: block applied and stored",
			ion.Uint64("block_number", b.BlockNumber), ion.String("block_hash", b.BlockHash.Hex()))
		return nil
	})
	helper.NotifyBroadcast(msg)
}
