package thebesync

// CatchUp is the ThebeSync (FastSync v4) catch-up entry point that replaces the
// FastsyncV2 Merkle-bisection catch-up. Both the CLI `catchup` command and the
// automatic syncmonitor ReconcileFunc route through here.

import (
	"context"
	"fmt"

	fssync "github.com/JupiterMetaLabs/JMDN-FastSync/thebesync"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// CatchUp brings the local node up to targetPeer's chain tip by log-shipping the
// certified block tail and applying it through the local verify+apply path
// (hybrid trust + P2.5 fingerprint halt). targetPeer is a libp2p multiaddr with an
// embedded peer ID, e.g. /ip4/192.168.1.5/tcp/15000/p2p/12D3KooW... Returns the
// height reached.
//
// Unlike the old engine, ThebeSync always syncs from the local tip+1 and verifies
// each block's parent linkage, so there is no bootstrap-gap concern and no
// fromBlock parameter is needed.
func CatchUp(ctx context.Context, h host.Host, targetPeer string) (uint64, error) {
	if h == nil {
		return 0, fmt.Errorf("thebesync catchup: nil host")
	}
	if targetPeer == "" {
		return 0, fmt.Errorf("thebesync catchup: empty target peer")
	}

	maddr, err := multiaddr.NewMultiaddr(targetPeer)
	if err != nil {
		return 0, fmt.Errorf("thebesync catchup: invalid multiaddr %q: %w", targetPeer, err)
	}
	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return 0, fmt.Errorf("thebesync catchup: extract peer info: %w", err)
	}
	if err := h.Connect(ctx, *info); err != nil {
		return 0, fmt.Errorf("thebesync catchup: connect to %s: %w", info.ID, err)
	}

	r := &fssync.Receiver{Applier: Applier{}}
	return r.SyncFrom(ctx, h, []peer.ID{info.ID})
}
