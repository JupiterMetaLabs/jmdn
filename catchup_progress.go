package main

// catchUpAdvanced reports whether a thebesync.CatchUp that returned without an
// error actually moved this node's chain forward.
//
// WHY THIS EXISTS. ThebeSync's Receiver.SyncFrom returns (localTip, nil) — a
// SUCCESS — as soon as the target peer's head handshake answers at or below the
// local tip ("caught up and stable (no new tail)"). The reconcile loops used to
// read only the error and stop at the first peer that did not fail, so a peer
// that served nothing ended the round and the remaining targets were never
// tried.
//
// That is reachable two ways. Benignly: a peer whose head regressed, or whose
// sampled height went stale, between PeerReporter's FetchHead and the sync's own
// head handshake — separate requests on separate streams, so they need not
// agree. Adversarially: a peer that reports a large height to the sampler so it
// is picked as a target, then answers the sync handshake at or below our tip.
// Because the peer-sampled reporter ranks the most advanced corroborated peers
// first, such a peer is tried first, and the node stays at its tip round after
// round while the monitor immediately re-checks and repeats.
//
// tipErr is the error from reading the local tip BEFORE the attempt. When the
// tip could not be read there is nothing to compare against, so this reports
// true and the caller keeps the previous behaviour rather than refusing a
// catch-up that may well have worked — a transient local read failure must not
// stall sync by itself.
func catchUpAdvanced(reached, tipBefore uint64, tipErr error) bool {
	if tipErr != nil {
		return true // cannot compare — do not block on a local read failure
	}
	return reached > tipBefore
}
