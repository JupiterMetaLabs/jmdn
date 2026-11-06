package thebesync

// Legacy follow mode, sync half (see messaging/legacy_follow.go): fetch the
// blocks a v3base node missed from the mainnet sequencer that still runs the
// main branch (main serves /fastsync/v4 with fastsync.serve_v4), and keep a
// connection to it so its live block pushes reach this node.
//
// TEMPORARY: remove with messaging/legacy_follow.go after the cutover.

import (
	"context"
	"fmt"
	"time"

	fssync "github.com/JupiterMetaLabs/JMDN-FastSync/thebesync"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"

	"github.com/JupiterMetaLabs/ion"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/logging"
	"gossipnode/messaging"
	"gossipnode/messaging/BlockProcessing"
)

// applyLegacyBlock applies one legacy block fetched from the main sequencer:
// contiguous linkage, the legacy body and equivocation gate, then the same
// apply+store path as any synced block.
func applyLegacyBlock(ctx context.Context, block *config.ZKBlock, prevNumber uint64, prevHash common.Hash) error {
	if block.BlockNumber != prevNumber+1 {
		return fmt.Errorf("thebesync legacy: non-contiguous block %d (expected %d)", block.BlockNumber, prevNumber+1)
	}
	if block.PrevHash != prevHash {
		return fmt.Errorf("thebesync legacy: block %d prevHash %s != last applied %s",
			block.BlockNumber, block.PrevHash.Hex(), prevHash.Hex())
	}
	if err := messaging.ValidateLegacySyncedBlock(ctx, block); err != nil {
		return fmt.Errorf("thebesync legacy: block %d: %w", block.BlockNumber, err)
	}
	if err := BlockProcessing.ProcessBlockTransactionsAndStore(ctx, block, nil, func() error {
		return DB_OPs.StoreZKBlock(nil, block)
	}); err != nil {
		return fmt.Errorf("thebesync legacy: block %d process+store: %w", block.BlockNumber, err)
	}
	messaging.NoteHeightCommitted(block.BlockNumber)
	messaging.NoteCommittedForCheckpoint(block)
	_, _, _ = DB_OPs.UpdateLatestBlockMonotonic(block.BlockNumber)
	return nil
}

// LegacyCatchUp fetches and applies every block the main sequencer has above
// this node's tip. Returns the height reached.
func LegacyCatchUp(ctx context.Context, h host.Host, main *peer.AddrInfo) (uint64, error) {
	h.Peerstore().AddAddrs(main.ID, main.Addrs, peerstore.PermanentAddrTTL)
	if err := h.Connect(ctx, *main); err != nil {
		return 0, fmt.Errorf("legacy follow: connect to main sequencer %s: %w", main.ID, err)
	}
	r := &fssync.Receiver{Applier: Applier{Source: main.ID}}
	return r.SyncFrom(ctx, h, []peer.ID{main.ID})
}

// legacyLogger returns the node's named logger for this package (nil-safe at
// call sites through logf).
func legacyLogger() *ion.Ion {
	l, err := logging.NewAsyncLogger().Get().NamedLogger(logging.Broadcast, "")
	if err != nil {
		return nil
	}
	return l.GetNamedLogger()
}

// LegacyFollower keeps a v3base node following the main sequencer.
type LegacyFollower struct {
	host     host.Host
	main     *peer.AddrInfo
	until    uint64
	interval time.Duration
	nudge    chan struct{}
	tipFn    func() (uint64, error)
	catchUp  func(ctx context.Context) (uint64, error)
}

// NewLegacyFollower validates the settings and builds the follower.
func NewLegacyFollower(h host.Host, s settings.LegacyFollowSettings) (*LegacyFollower, error) {
	main, err := messaging.ParseLegacyMainSequencer(s.MainSequencer)
	if err != nil {
		return nil, err
	}
	interval := s.CatchUpInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	f := &LegacyFollower{host: h, main: main, until: s.UntilHeight, interval: interval, nudge: make(chan struct{}, 1)}
	f.tipFn = func() (uint64, error) { return DB_OPs.GetLatestBlockNumber(context.Background(), nil) }
	f.catchUp = func(ctx context.Context) (uint64, error) { return LegacyCatchUp(ctx, f.host, f.main) }
	return f, nil
}

// Nudge asks the follower to check the main sequencer now (non-blocking).
func (f *LegacyFollower) Nudge() {
	select {
	case f.nudge <- struct{}{}:
	default:
	}
}

// done reports whether the legacy window is over for this node.
func (f *LegacyFollower) done() bool {
	if f.until == 0 {
		return false
	}
	tip, err := f.tipFn()
	return err == nil && tip >= f.until
}

// Run catches up on start, then on every interval or nudge, until ctx ends or
// the node's tip reaches legacy_follow.until_height. The connection to the
// main sequencer is protected from the connection manager so its live block
// pushes keep arriving.
func (f *LegacyFollower) Run(ctx context.Context) {
	if f.host != nil {
		f.host.ConnManager().Protect(f.main.ID, "legacy-follow")
		defer f.host.ConnManager().Unprotect(f.main.ID, "legacy-follow")
	}
	t := time.NewTicker(f.interval)
	defer t.Stop()
	for {
		if f.done() {
			if l := legacyLogger(); l != nil {
				l.Info(ctx, "[legacy-follow] reached until_height; legacy follow finished", ion.Uint64("until_height", f.until))
			}
			return
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		height, err := f.catchUp(cctx)
		cancel()
		if l := legacyLogger(); l != nil {
			if err != nil && ctx.Err() == nil {
				l.Warn(ctx, "[legacy-follow] catch-up from the main sequencer failed (will retry)",
					ion.Err(err), ion.String("main_sequencer", f.main.ID.String()))
			} else if err == nil {
				l.Debug(ctx, "[legacy-follow] caught up with the main sequencer", ion.Uint64("height", height))
			}
		}
		if f.done() {
			continue // logged and returned at the top of the loop
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-f.nudge:
		}
	}
}
