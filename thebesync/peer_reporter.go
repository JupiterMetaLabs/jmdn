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
// height at least two peers report as a leaderless stand-in for
// "SequencerHead" (see selectCatchUpTargets). It lags the true fleet tip when
// the sampled peers are themselves behind. A single peer cannot move the head;
// two colluding peers can inflate it, which costs extra catch-up attempts but
// never removes honest peers from the targets, and the head is never trusted
// for voting (HeadAuthenticated is false, below).
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
// head and reports the highest height at least two of them agree on as
// SequencerHead (selectCatchUpTargets). blockHead is this node's own tip
// (supplied by the Monitor from ChainReporter.TipState); it decides IsSynced
// and which peers are catch-up targets — there is no seednode to send it to.
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

	best, targets := selectCatchUpTargets(results, blockHead)

	goodPeers := make([]syncmonitor.PeerInfo, 0, len(targets))
	for _, r := range targets {
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
	// `best` is the highest height claimed by at least two of up to 8 sampled
	// peers (selectCatchUpTargets), over an unauthenticated wire — two peers'
	// answers set it.
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
			"head=%d (2 peers agree when 2+ answered) local=%d — UNAUTHENTICATED, not eligible to drive the consensus vote gate",
			len(results), len(peers), best, blockHead),
	}, nil
}

// selectCatchUpTargets picks the reported head and the ordered catch-up
// targets from one round of samples.
//
// The head is the highest height at least two peers report (the second-highest
// sample), or the only sample when just one peer answered. Taking the bare
// maximum let one peer claiming a huge height become the head AND, since only
// peers at the head were targets, the sole catch-up source, starving the node
// of its honest peers.
//
// Targets are every peer above ourTip, lowest height first, not only peers at
// the head: two peers (libp2p identities are free, so one attacker can be two)
// agreeing on a huge height must not push the honest peers out of the list.
// The reconcile loop (main.go) stops at the first CatchUp that returns without
// error, so order matters, not just membership: honest peers claiming ordinary
// heights are tried before any inflated claim. An empty list just means no
// sampled peer is ahead of us.
func selectCatchUpTargets(results []peerHeadSample, ourTip uint64) (uint64, []peerHeadSample) {
	if len(results) == 0 {
		return 0, nil
	}
	sorted := append([]peerHeadSample(nil), results...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].height < sorted[j].height })

	best := sorted[len(sorted)-1].height
	if len(sorted) >= 2 {
		best = sorted[len(sorted)-2].height
	}

	targets := make([]peerHeadSample, 0, len(sorted))
	for _, r := range sorted {
		if r.height > ourTip {
			targets = append(targets, r)
		}
	}
	return best, targets
}
