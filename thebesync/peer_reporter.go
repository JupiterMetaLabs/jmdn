package thebesync

// PeerReporter is a syncmonitor.SeedReporter backed directly by connected libp2p
// peers instead of a seednode.
//
// Why this exists: the periodic syncmonitor.Monitor (see internal/syncmonitor)
// only ever gets constructed when cfg.Network.SeedNode is configured and its
// client can be created (main.go). When it is not — a valid, supported
// topology; main.go logs a warning and continues rather than failing — no
// periodic self-check of any kind ran before this file. Catch-up only fired
// REACTIVELY, from messaging.checkLinkage's height-gap detection on a NEWLY
// ARRIVED block (messaging/consensus_hardening.go). A node whose block
// delivery silently stalled (peers stopped forwarding to it, its own
// subscription died, a brief partition) had no path back, because the very
// mechanism meant to notice the gap needed a new block to arrive in order to
// run at all. Peer connectivity heartbeats (node/nodemanager.go) do not help
// either — they check liveness of the connection, never whether blocks are
// still flowing over it.
//
// PeerReporter closes that gap by sampling a bounded set of currently
// connected peers for their head (thebesync.FetchHead — the same wire call
// the seednode-backed reconcile path already trusts) and treating the highest
// reported height as a leaderless stand-in for "SequencerHead". This is
// conservative by construction: at worst it lags the true fleet tip (if the
// sampled peers are themselves behind), never ahead of it, so it can only
// ever under-report a divergence, never manufacture a false one.
//
// Wiring this into a syncmonitor.Monitor (rather than writing a second,
// bespoke periodic loop) reuses ALL of that Monitor's existing hardening for
// free: startup jitter, the in-flight-write propagation guard, the
// consecutive-out-of-sync threshold, the adaptive check interval, and the
// single-flight reconcile guard. Only the reporter differs.
import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"gossipnode/internal/syncmonitor"

	fssync "github.com/JupiterMetaLabs/JMDN-FastSync/thebesync"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// maxPeerReporterSample bounds how many connected peers a single check queries.
// Matches this codebase's existing convention for bounded peer fan-out during
// recovery (cf. maxRecoveryPeers in messaging/entropy_vdf_pull.go): legitimate
// use is a handful of peers per check, not the whole connected set, and an
// unbounded fan-out here would turn every sync tick into a thundering herd of
// outbound streams as the peer count grows.
const maxPeerReporterSample = 8

// peerHeadFetchTimeout bounds a single peer's FetchHead call so one slow or
// dead peer cannot stall an entire check cycle.
const peerHeadFetchTimeout = 5 * time.Second

// PeerReporter implements syncmonitor.SeedReporter over directly-connected
// peers. Host must be non-nil before ReportBlockState is called.
type PeerReporter struct {
	Host host.Host
}

// peerHeadSample is one peer's answer to the head handshake.
type peerHeadSample struct {
	id     peer.ID
	height uint64
}

// ReportBlockState asks a bounded sample of connected peers for their chain
// head and reports the highest one back as SequencerHead. blockHead is this
// node's own tip (supplied by the Monitor from ChainReporter.TipState) and is
// used only for the IsSynced comparison — there is no seednode to send it to.
func (p PeerReporter) ReportBlockState(ctx context.Context, blockHead uint64, _ []byte) (*syncmonitor.SyncStatus, error) {
	if p.Host == nil {
		return nil, fmt.Errorf("thebesync: PeerReporter has a nil host")
	}

	peers := p.Host.Network().Peers()
	if len(peers) == 0 {
		return nil, fmt.Errorf("thebesync: PeerReporter has no connected peers to sample")
	}
	if len(peers) > maxPeerReporterSample {
		peers = peers[:maxPeerReporterSample]
	}

	results := make([]peerHeadSample, 0, len(peers))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, pid := range peers {
		pid := pid
		wg.Add(1)
		go func() {
			defer wg.Done()
			reqCtx, cancel := context.WithTimeout(ctx, peerHeadFetchTimeout)
			defer cancel()
			resp, err := fssync.FetchHead(reqCtx, p.Host, pid)
			if err != nil {
				// Includes both a transport failure and the peer's own
				// resp.Error case (FetchHead already folds that into err) —
				// either way this peer is not usable this cycle.
				return
			}
			mu.Lock()
			results = append(results, peerHeadSample{id: pid, height: resp.Height})
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(results) == 0 {
		return nil, fmt.Errorf("thebesync: PeerReporter got no usable head from %d sampled peer(s)", len(peers))
	}

	sort.Slice(results, func(i, j int) bool { return results[i].height > results[j].height })
	best := results[0].height

	// GoodPeers carries only peers AT the best height: the reconcile path
	// dials whichever one answers first, so a peer reported behind the best
	// sample is not a usable catch-up target.
	goodPeers := make([]syncmonitor.PeerInfo, 0, len(results))
	for _, r := range results {
		if r.height < best {
			continue
		}
		addrs := p.Host.Peerstore().Addrs(r.id)
		multiaddrs := make([]string, 0, len(addrs))
		for _, a := range addrs {
			multiaddrs = append(multiaddrs, a.String())
		}
		if len(multiaddrs) == 0 {
			continue // nothing to dial — not a usable reconcile target
		}
		goodPeers = append(goodPeers, syncmonitor.PeerInfo{PeerID: r.id.String(), Multiaddrs: multiaddrs})
	}

	// HeadAuthenticated is FALSE, and that is the load-bearing line here.
	//
	// `best` is the MAXIMUM height claimed by up to 8 sampled peers, over an
	// unauthenticated wire, with no corroboration — one peer's answer sets it.
	// That is fine for deciding "should I catch up?", which is all this reporter
	// was built for, and NOT fine for deciding "should I vote?".
	//
	// The second question is real: main.go feeds SequencerHead to
	// MessagePassing.SetConsensusSyncGate, and GateDecision ->
	// ConsensusVoteEligible ABSTAINS once sequencerHead-localHead exceeds
	// MaxConsensusLagBlocks, which is 2. That gate is consulted on the live vote
	// path (ListenerHandler.go's handleVoteResultRequest) and is enforced by
	// default (JMDN_ENFORCE_SYNC_GATE != "0"). Publishing `best` as an
	// authenticated head would therefore let a SINGLE peer answering FetchHead
	// with localHead+3 silently pull this node out of consensus — no stake, no
	// committee seat, and a number small enough to look like ordinary lag. The
	// seednode-backed reporter this one stands in for does not have that
	// property: its head comes from one accountable source.
	//
	// The head is still REPORTED, because the monitor's propagation-lag filter
	// needs it to tell a block-in-flight from a real divergence. Suppressing it
	// (SequencerHead: 0) would disable that filter and turn every one-block lag
	// into a catch-up. So the split is: publish the number, refuse to vouch for
	// it, and let each consumer decide — sync tuning may use it, consensus
	// participation may not.
	//
	// DO NOT set HeadAuthenticated true here without replacing `best` with a
	// head that is either authenticated or corroborated by a quorum of peers. A
	// bare maximum over unauthenticated samples cannot carry that claim.
	return &syncmonitor.SyncStatus{
		IsSynced:          blockHead >= best,
		SequencerHead:     best,
		HeadAuthenticated: false,
		GoodPeers:         goodPeers,
		Message: fmt.Sprintf("peer-sampled head (no seednode configured): best of %d/%d sampled peer(s) responded, "+
			"highest=%d local=%d — UNAUTHENTICATED, not eligible to drive the consensus vote gate",
			len(results), len(peers), best, blockHead),
	}, nil
}
