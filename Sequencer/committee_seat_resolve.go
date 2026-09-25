package Sequencer

// Seat address resolution (committee-v2 seat/candidate mismatch fix).
//
// THE DEFECT
//
// Under JMDN_COMMITTEE_V2 two different lists decide a round:
//
//   - WHO COUNTS: messaging.SelectCommittee draws k seats from the seed-signed
//     snapshot. That draw is reputation-blind (UniformSelectionWeight).
//   - WHO CAN BE DIALLED: the warmup candidate pool comes from NodeSelection,
//     which drops every peer whose seed weight is outside the selection band
//     (AVC/NodeSelection/pkg/selection/filter.go). It is reputation-FILTERED.
//
// A seated peer that the band dropped has no multiaddr in the pool, so
// OrderCandidatesBySeat reports it as a missing seat, it is never dialled, and
// SetZKBlockData leaves it off the wire list while it still counts toward n.
// When enough seats are missing the certificate can never reach 2f+1 - the
// testnet stall at blocks 836/848 (missing_seats: 7, dialable: 0).
//
// THE FIX
//
// The seed already holds every peer's multiaddrs; the pool just filtered them
// away. Before the seat order is taken, look each missing seat up in the
// seed's ListBuddy address book and add it to the candidate pool so it is
// dialled like any other candidate. Seats are the authority on who must be
// reached; reputation weight no longer decides whether a seat can vote.
//
// WHAT THIS DOES NOT CHANGE
//
//   - The tally. Votes are still counted only against the seated committee
//     (messaging.VerifyCertificateForRound). Nothing here lets a non-seated
//     peer's vote count, and nothing lowers n.
//   - Authorization. Only seats are ever added, and only seats that are also
//     in the pinned eligible (signed) set when one is supplied, so the
//     "selection is a subset of authorization" invariant from the
//     Committee-source filter in Consensus.Start still holds.
//   - Behaviour with JMDN_COMMITTEE_V2 off: the caller only runs this inside
//     the v2 seat-order block.
//   - Liveness on failure: if the seed cannot be reached, nothing is added and
//     the round proceeds exactly as it did before this fix.

import (
	"context"
	"fmt"
	"sync"

	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"
	"gossipnode/seednode"
	peerpb "gossipnode/seednode/proto"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// SeatResolution reports what ResolveMissingSeats did, for logging. Every
// seat that was missing from the candidate pool lands in exactly one list.
type SeatResolution struct {
	// Added: seats appended to the pool with an address from the book.
	Added []string
	// NoAddress: seats with no entry in the book, or none of whose
	// multiaddrs parse and belong to that peer.
	NoAddress []string
	// Unauthorized: seats outside the supplied eligible set. Never dialled.
	Unauthorized []string
}

// Resolved reports whether every missing seat was added.
func (r SeatResolution) Resolved() bool {
	return len(r.NoAddress) == 0 && len(r.Unauthorized) == 0
}

// MissingSeatIDs returns the peer id of every dial target that has no entry in
// candidates, in seat order. Unparseable seat ids are not reported here:
// OrderCandidatesBySeat already reports them as missing, and no address could
// be dialled for them anyway.
func MissingSeatIDs(
	candidates []PubSubMessages.Buddy_PeerMultiaddr,
	dialTargets []committee.Member,
) []string {
	have := make(map[peer.ID]struct{}, len(candidates))
	for _, c := range candidates {
		have[c.PeerID] = struct{}{}
	}
	var missing []string
	seen := make(map[peer.ID]struct{}, len(dialTargets))
	for _, m := range dialTargets {
		pid, err := peer.Decode(m.PeerID)
		if err != nil {
			continue
		}
		if _, dup := seen[pid]; dup {
			continue
		}
		seen[pid] = struct{}{}
		if _, ok := have[pid]; !ok {
			missing = append(missing, m.PeerID)
		}
	}
	return missing
}

// ResolveMissingSeats returns candidates with every dial target that is not
// already a candidate appended, using the first usable multiaddr from
// addrBook (peer id -> multiaddr strings, as the seed's ListBuddy returns
// them). It never removes, reorders or rewrites an existing candidate, and the
// input slice is not modified.
//
// A multiaddr is usable when it parses and, if it carries a /p2p/ component,
// that component names the seated peer itself: an address claiming a
// different identity is skipped rather than dialled.
//
// eligible, when non-nil, is the pinned signed eligible set; a seat outside it
// is reported Unauthorized and not added. When nil no extra filter is applied
// - seats come from the authenticated snapshot already (SelectCommittee
// refuses to run under v2 without a pinned seed authority).
//
// The address is taken the same way the warmup path takes it (the first
// entry per peer - see helper.GetUniqueBuddyPeers), so a resolved seat is
// dialled exactly as it would have been had the band not dropped it.
func ResolveMissingSeats(
	candidates []PubSubMessages.Buddy_PeerMultiaddr,
	dialTargets []committee.Member,
	addrBook map[string][]string,
	eligible map[string]struct{},
) ([]PubSubMessages.Buddy_PeerMultiaddr, SeatResolution) {
	var res SeatResolution
	out := make([]PubSubMessages.Buddy_PeerMultiaddr, len(candidates), len(candidates)+len(dialTargets))
	copy(out, candidates)

	for _, id := range MissingSeatIDs(candidates, dialTargets) {
		if eligible != nil {
			if _, ok := eligible[id]; !ok {
				res.Unauthorized = append(res.Unauthorized, id)
				continue
			}
		}
		pid, err := peer.Decode(id)
		if err != nil {
			// MissingSeatIDs never returns an undecodable id; kept total anyway.
			res.NoAddress = append(res.NoAddress, id)
			continue
		}
		addr, ok := firstUsableAddr(pid, addrBook[id])
		if !ok {
			res.NoAddress = append(res.NoAddress, id)
			continue
		}
		out = append(out, PubSubMessages.Buddy_PeerMultiaddr{PeerID: pid, Multiaddr: addr})
		res.Added = append(res.Added, id)
	}
	return out, res
}

// firstUsableAddr returns the first multiaddr in addrs that parses and does
// not name a different peer in a /p2p/ component.
func firstUsableAddr(pid peer.ID, addrs []string) (multiaddr.Multiaddr, bool) {
	for _, s := range addrs {
		ma, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			continue
		}
		if _, embedded := peer.SplitAddr(ma); embedded != "" && embedded != pid {
			continue
		}
		return ma, true
	}
	return nil, false
}

// seatAddressBook fetches peer id -> multiaddrs from the seed's ListBuddy.
// A package variable so tests can substitute it; production uses
// fetchSeatAddressBook.
var seatAddressBook = fetchSeatAddressBook

// seedClientDialer creates the seed gRPC client used by fetchSeatAddressBook.
// A package variable (like seatAddressBook above) purely so tests can count
// or fake dial calls without a live network; production always uses
// seednode.NewClient.
var seedClientDialer = seednode.NewClient

// F-8 / A-11 (pure optimization, no behaviour change): fetchSeatAddressBook
// used to call seednode.NewClient - a fresh grpc.Dial + TLS handshake - and
// Close it again on every single call, i.e. every round that has a missing
// seat. That is needless churn on the consensus critical path (this call sits
// behind a 3s timeout inside a 45s round budget, Consensus.go:254/2111) for a
// connection that grpc.ClientConn is explicitly designed to have dialled once
// and reused: it is safe for concurrent RPCs and reconnects internally on
// transient failures (see google.golang.org/grpc's own docs on ClientConn
// lifecycle). So this now dials once and keeps the client for the life of the
// process, only redialling if the configured seed URL itself changes (a
// config reload/rotation, not a per-round event).
var (
	seatAddressBookClientMu  sync.Mutex
	seatAddressBookClient    *seednode.Client
	seatAddressBookClientURL string
)

// seatAddressBookClientFor returns the cached seed client for addr, dialling
// (and caching) a new one only on the first call or after addr changes from
// what is currently cached. Safe for concurrent callers.
func seatAddressBookClientFor(addr string) (*seednode.Client, error) {
	seatAddressBookClientMu.Lock()
	defer seatAddressBookClientMu.Unlock()

	if seatAddressBookClient != nil && seatAddressBookClientURL == addr {
		return seatAddressBookClient, nil
	}
	if seatAddressBookClient != nil {
		// The seed URL changed under us (config reload) - close the old
		// connection rather than leaking it, then dial the new one.
		_ = seatAddressBookClient.Close()
		seatAddressBookClient = nil
		seatAddressBookClientURL = ""
	}

	sc, err := seedClientDialer(addr)
	if err != nil {
		return nil, err
	}
	seatAddressBookClient = sc
	seatAddressBookClientURL = addr
	return sc, nil
}

// resetSeatAddressBookClient closes and forgets any cached seed client.
// Tests use it to get a clean cache between cases; production code has no
// reason to call it (a URL change is handled automatically by
// seatAddressBookClientFor).
func resetSeatAddressBookClient() {
	seatAddressBookClientMu.Lock()
	defer seatAddressBookClientMu.Unlock()
	if seatAddressBookClient != nil {
		_ = seatAddressBookClient.Close()
	}
	seatAddressBookClient = nil
	seatAddressBookClientURL = ""
}

// fetchSeatAddressBook calls the seed's ListBuddy - the same RPC, and the same
// automatic sequencer authentication (seednode.Client.ListBuddy), that the
// warmup NodeSelection path and the buddy-head enrichment already use. It
// returns every record, before any selection-band filtering.
//
// The gRPC client itself is cached across calls (see seatAddressBookClientFor
// above); this function no longer dials or closes a connection on every
// invocation, only on the first one (or after a seed URL change).
func fetchSeatAddressBook(ctx context.Context) (map[string][]string, error) {
	if !settings.IsLoaded() {
		return nil, fmt.Errorf("settings not loaded")
	}
	addr := settings.Get().Network.SeedNode
	if addr == "" {
		return nil, fmt.Errorf("no seednode URL configured")
	}
	sc, err := seatAddressBookClientFor(addr)
	if err != nil {
		return nil, fmt.Errorf("seed client init: %w", err)
	}

	resp, err := sc.ListBuddy(ctx, &peerpb.ListBuddyRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListBuddy: %w", err)
	}
	book := make(map[string][]string, len(resp.GetPeers()))
	for _, p := range resp.GetPeers() {
		id := p.GetPeerId()
		if id == "" || len(p.GetMultiaddrs()) == 0 {
			continue
		}
		book[id] = append([]string(nil), p.GetMultiaddrs()...)
	}
	return book, nil
}
