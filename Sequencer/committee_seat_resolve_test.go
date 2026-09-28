package Sequencer

import (
	"fmt"
	"reflect"
	"testing"

	PubSubMessages "gossipnode/config/PubSubMessages"
	"gossipnode/seednode"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// bookFor builds a seed-style address book (peer id -> multiaddr strings) for
// ids, on a distinct subnet from candidatesFor so tests can tell which source
// an address came from.
func bookFor(ids []peer.ID) map[string][]string {
	book := make(map[string][]string, len(ids))
	for i, pid := range ids {
		book[pid.String()] = []string{fmt.Sprintf("/ip4/10.90.1.%d/tcp/15000", i+1)}
	}
	return book
}

func eligibleFor(ids []peer.ID) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, pid := range ids {
		out[pid.String()] = struct{}{}
	}
	return out
}

// TestResolveMissingSeats_ReproducesTestnetStall models the observed halt:
// 29 eligible peers, a reputation-filtered pool of 7, and 7 seats that are all
// outside the pool (missing_seats: 7). After resolution every seat is in the
// pool, and after the seat order + reachability projection + MaxMainPeers
// split that Consensus.Start performs, MainCandidates is exactly the seated
// committee - so SetZKBlockData's seated filter keeps all 7 (dialable == seated).
func TestResolveMissingSeats_ReproducesTestnetStall(t *testing.T) {
	const (
		eligibleN = 29
		poolN     = 7
		k         = 7 // config.MaxMainPeers
	)
	ids := testPeers(t, eligibleN)
	pool := candidatesFor(t, ids[:poolN])
	seatPeers := []peer.ID{ids[27], ids[9], ids[22], ids[14], ids[28], ids[11], ids[18]}
	seats := seatsFor(seatPeers)

	// Before the fix: every seat is missing.
	if _, missing := OrderCandidatesBySeat(pool, seats); len(missing) != k {
		t.Fatalf("precondition: want %d missing seats, got %d", k, len(missing))
	}

	resolved, res := ResolveMissingSeats(pool, seats, bookFor(ids), eligibleFor(ids))
	if !res.Resolved() || len(res.Added) != k {
		t.Fatalf("want all %d seats added, got %+v", k, res)
	}
	ordered, missing := OrderCandidatesBySeat(resolved, seats)
	if len(missing) != 0 {
		t.Fatalf("after resolution want 0 missing seats, got %v", missing)
	}

	// Everything reachable; project and split exactly as Consensus.Start does.
	reachable := make(map[peer.ID]multiaddr.Multiaddr, len(ordered))
	for _, c := range ordered {
		reachable[c.PeerID] = c.Multiaddr
	}
	probe := ReachableInCandidateOrder(ordered, reachable)
	if len(probe) < k {
		t.Fatalf("probe too short: %d", len(probe))
	}
	main := probe[:k]
	for i, pid := range seatPeers {
		if main[i].PeerID != pid {
			t.Fatalf("MainCandidates[%d] = %s, want seat %s", i, main[i].PeerID, pid)
		}
	}
}

func TestResolveMissingSeats_KeepsExistingCandidatesUntouched(t *testing.T) {
	ids := testPeers(t, 10)
	pool := candidatesFor(t, ids[:5])
	orig := append([]PubSubMessages.Buddy_PeerMultiaddr(nil), pool...)
	// Seats: two already in the pool, two not.
	seats := seatsFor([]peer.ID{ids[1], ids[7], ids[3], ids[8]})

	out, res := ResolveMissingSeats(pool, seats, bookFor(ids), eligibleFor(ids))

	if !reflect.DeepEqual(pool, orig) {
		t.Fatalf("input slice was modified")
	}
	if !reflect.DeepEqual(out[:5], orig) {
		t.Fatalf("existing candidates were changed or reordered")
	}
	if want := []string{ids[7].String(), ids[8].String()}; !reflect.DeepEqual(res.Added, want) {
		t.Fatalf("added = %v, want %v (seat order)", res.Added, want)
	}
	if len(out) != 7 {
		t.Fatalf("want 7 candidates, got %d", len(out))
	}
	// The pool's own address for a seat that was already present is kept.
	if out[1].Multiaddr.String() != orig[1].Multiaddr.String() {
		t.Fatalf("existing seat address replaced: %s", out[1].Multiaddr)
	}
	// A resolved seat carries the seed book's address.
	if got, want := out[5].Multiaddr.String(), "/ip4/10.90.1.8/tcp/15000"; got != want {
		t.Fatalf("resolved address = %s, want %s", got, want)
	}
}

func TestResolveMissingSeats_UnauthorizedSeatIsNotAdded(t *testing.T) {
	ids := testPeers(t, 6)
	pool := candidatesFor(t, ids[:2])
	seats := seatsFor([]peer.ID{ids[4], ids[5]})
	eligible := eligibleFor(ids[:5]) // ids[5] is not in the signed set

	out, res := ResolveMissingSeats(pool, seats, bookFor(ids), eligible)

	if want := []string{ids[5].String()}; !reflect.DeepEqual(res.Unauthorized, want) {
		t.Fatalf("unauthorized = %v, want %v", res.Unauthorized, want)
	}
	for _, c := range out {
		if c.PeerID == ids[5] {
			t.Fatalf("unauthorized seat was added to the pool")
		}
	}
	if len(res.Added) != 1 || res.Resolved() {
		t.Fatalf("unexpected resolution %+v", res)
	}
}

func TestResolveMissingSeats_NoUsableAddress(t *testing.T) {
	ids := testPeers(t, 4)
	pool := candidatesFor(t, ids[:1])
	seats := seatsFor([]peer.ID{ids[1], ids[2], ids[3]})
	book := map[string][]string{
		// ids[1]: absent from the book entirely.
		ids[2].String(): {"not-a-multiaddr", ""},
		ids[3].String(): {},
	}

	out, res := ResolveMissingSeats(pool, seats, book, eligibleFor(ids))

	if len(res.Added) != 0 || len(out) != 1 {
		t.Fatalf("nothing should be added: %+v, len(out)=%d", res, len(out))
	}
	want := []string{ids[1].String(), ids[2].String(), ids[3].String()}
	if !reflect.DeepEqual(res.NoAddress, want) {
		t.Fatalf("no_address = %v, want %v", res.NoAddress, want)
	}
}

func TestResolveMissingSeats_RejectsAddressNamingAnotherPeer(t *testing.T) {
	ids := testPeers(t, 3)
	seat, other := ids[1], ids[2]
	seats := seatsFor([]peer.ID{seat})
	book := map[string][]string{
		seat.String(): {
			"/ip4/10.90.1.1/tcp/15000/p2p/" + other.String(), // wrong identity: skipped
			"/ip4/10.90.1.2/tcp/15000/p2p/" + seat.String(),  // own identity: used
		},
	}

	out, res := ResolveMissingSeats(nil, seats, book, nil)

	if len(res.Added) != 1 || len(out) != 1 {
		t.Fatalf("want the seat added once, got %+v", res)
	}
	if got := out[0].Multiaddr.String(); got != book[seat.String()][1] {
		t.Fatalf("used %s, want the address carrying the seat's own id", got)
	}

	// Only a wrong-identity address: the seat must stay unresolved.
	book[seat.String()] = book[seat.String()][:1]
	_, res = ResolveMissingSeats(nil, seats, book, nil)
	if len(res.Added) != 0 || len(res.NoAddress) != 1 {
		t.Fatalf("wrong-identity address must not be dialled: %+v", res)
	}
}

func TestResolveMissingSeats_NothingMissingIsIdentity(t *testing.T) {
	ids := testPeers(t, 5)
	pool := candidatesFor(t, ids)
	seats := seatsFor(ids[:3])

	if m := MissingSeatIDs(pool, seats); len(m) != 0 {
		t.Fatalf("MissingSeatIDs = %v, want none", m)
	}
	out, res := ResolveMissingSeats(pool, seats, nil, nil)
	if !reflect.DeepEqual(out, pool) || len(res.Added)+len(res.NoAddress)+len(res.Unauthorized) != 0 {
		t.Fatalf("expected identity, got %+v", res)
	}
}

func TestResolveMissingSeats_DuplicateSeatAddedOnce(t *testing.T) {
	ids := testPeers(t, 3)
	seats := seatsFor([]peer.ID{ids[2], ids[2]})

	out, res := ResolveMissingSeats(candidatesFor(t, ids[:1]), seats, bookFor(ids), nil)

	if len(res.Added) != 1 || len(out) != 2 {
		t.Fatalf("duplicate seat must be added once: %+v, len(out)=%d", res, len(out))
	}
}

// TestSeatAddressBookClientFor_CachesAndRedialsOnURLChange is the A-11/F-8
// regression test: the seed gRPC client must be dialled once and reused
// across rounds, not rebuilt on every call, and must only redial when the
// configured seed URL actually changes.
func TestSeatAddressBookClientFor_CachesAndRedialsOnURLChange(t *testing.T) {
	resetSeatAddressBookClient()
	defer resetSeatAddressBookClient()

	dials := 0
	orig := seedClientDialer
	seedClientDialer = func(addr string) (*seednode.Client, error) {
		dials++
		return orig(addr)
	}
	defer func() { seedClientDialer = orig }()

	c1, err := seatAddressBookClientFor("127.0.0.1:0")
	if err != nil {
		t.Fatalf("seatAddressBookClientFor: %v", err)
	}
	c2, err := seatAddressBookClientFor("127.0.0.1:0")
	if err != nil {
		t.Fatalf("seatAddressBookClientFor (2nd call, same URL): %v", err)
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1 (same URL must reuse the cached client, not redial)", dials)
	}
	if c1 != c2 {
		t.Fatalf("seatAddressBookClientFor returned a different instance for an unchanged URL")
	}

	c3, err := seatAddressBookClientFor("127.0.0.1:1")
	if err != nil {
		t.Fatalf("seatAddressBookClientFor (new URL): %v", err)
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2 (a seed URL change must redial)", dials)
	}
	if c3 == c2 {
		t.Fatalf("expected a new client instance after the seed URL changed")
	}
}

func TestMissingSeatIDs_SkipsUnparseableSeats(t *testing.T) {
	ids := testPeers(t, 2)
	seats := append(seatsFor(ids), committee.Member{PeerID: "not-a-peer-id"})

	got := MissingSeatIDs(nil, seats)
	if want := []string{ids[0].String(), ids[1].String()}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingSeatIDs = %v, want %v", got, want)
	}
}
