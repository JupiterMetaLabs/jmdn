package Sequencer

import (
	"fmt"
	mrand "math/rand"
	"reflect"
	"sort"
	"testing"

	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// testPeers returns n deterministic peer ids. Deterministic so a failure is
// reproducible; the tests never depend on the ids' relative values except where
// they sort explicitly.
func testPeers(t *testing.T, n int) []peer.ID {
	t.Helper()
	src := mrand.New(mrand.NewSource(20260907))
	ids := make([]peer.ID, 0, n)
	for i := 0; i < n; i++ {
		_, pub, err := crypto.GenerateEd25519Key(src)
		if err != nil {
			t.Fatalf("generate key %d: %v", i, err)
		}
		pid, err := peer.IDFromPublicKey(pub)
		if err != nil {
			t.Fatalf("peer id %d: %v", i, err)
		}
		ids = append(ids, pid)
	}
	return ids
}

func candidatesFor(t *testing.T, ids []peer.ID) []PubSubMessages.Buddy_PeerMultiaddr {
	t.Helper()
	out := make([]PubSubMessages.Buddy_PeerMultiaddr, 0, len(ids))
	for i, pid := range ids {
		ma, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/10.0.0.%d/tcp/15000", i+1))
		if err != nil {
			t.Fatalf("multiaddr %d: %v", i, err)
		}
		out = append(out, PubSubMessages.Buddy_PeerMultiaddr{PeerID: pid, Multiaddr: ma})
	}
	return out
}

func seatsFor(ids []peer.ID) []committee.Member {
	out := make([]committee.Member, 0, len(ids))
	for _, pid := range ids {
		out = append(out, committee.Member{PeerID: pid.String()})
	}
	return out
}

func peerIDs(cands []PubSubMessages.Buddy_PeerMultiaddr) []peer.ID {
	out := make([]peer.ID, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.PeerID)
	}
	return out
}

// assertPermutation is the non-lossy invariant: reordering must not add, drop
// or duplicate a candidate.
func assertPermutation(t *testing.T, got, want []PubSubMessages.Buddy_PeerMultiaddr) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length changed: got %d, want %d", len(got), len(want))
	}
	countOf := func(cs []PubSubMessages.Buddy_PeerMultiaddr) map[peer.ID]int {
		m := make(map[peer.ID]int, len(cs))
		for _, c := range cs {
			m[c.PeerID]++
		}
		return m
	}
	if g, w := countOf(got), countOf(want); !reflect.DeepEqual(g, w) {
		t.Fatalf("not a permutation:\n got %v\nwant %v", g, w)
	}
}

func TestOrderCandidatesBySeat_SeatedFirstInSeatOrder(t *testing.T) {
	ids := testPeers(t, 5)
	cands := candidatesFor(t, ids)

	// Seat order deliberately disagrees with candidate order.
	seated := seatsFor([]peer.ID{ids[3], ids[0], ids[4]})

	ordered, missing := OrderCandidatesBySeat(cands, seated)
	if len(missing) != 0 {
		t.Fatalf("expected no missing seats, got %v", missing)
	}
	assertPermutation(t, ordered, cands)

	want := []peer.ID{ids[3], ids[0], ids[4], ids[1], ids[2]}
	if got := peerIDs(ordered); !reflect.DeepEqual(got, want) {
		t.Fatalf("order mismatch:\n got %v\nwant %v", got, want)
	}

	// The multiaddr must travel with the peer, not with the position.
	for _, c := range ordered {
		for _, orig := range cands {
			if orig.PeerID == c.PeerID && !orig.Multiaddr.Equal(c.Multiaddr) {
				t.Fatalf("multiaddr for %s changed: got %s, want %s",
					c.PeerID, c.Multiaddr, orig.Multiaddr)
			}
		}
	}
}

func TestOrderCandidatesBySeat_ReportsSeatsNotInPool(t *testing.T) {
	ids := testPeers(t, 6)
	// Candidates hold only the first four; two seats are outside the pool.
	cands := candidatesFor(t, ids[:4])
	seated := seatsFor([]peer.ID{ids[2], ids[4], ids[5], ids[1]})

	ordered, missing := OrderCandidatesBySeat(cands, seated)

	assertPermutation(t, ordered, cands)
	wantOrder := []peer.ID{ids[2], ids[1], ids[0], ids[3]}
	if got := peerIDs(ordered); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("order mismatch:\n got %v\nwant %v", got, wantOrder)
	}
	wantMissing := []string{ids[4].String(), ids[5].String()}
	if !reflect.DeepEqual(missing, wantMissing) {
		t.Fatalf("missing seats mismatch:\n got %v\nwant %v", missing, wantMissing)
	}
}

func TestOrderCandidatesBySeat_UnparseableSeatIsMissingNotFatal(t *testing.T) {
	ids := testPeers(t, 3)
	cands := candidatesFor(t, ids)
	seated := []committee.Member{
		{PeerID: "not-a-peer-id"},
		{PeerID: ids[2].String()},
		{PeerID: ""},
	}

	ordered, missing := OrderCandidatesBySeat(cands, seated)

	assertPermutation(t, ordered, cands)
	if got, want := peerIDs(ordered), []peer.ID{ids[2], ids[0], ids[1]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order mismatch:\n got %v\nwant %v", got, want)
	}
	if want := []string{"not-a-peer-id", ""}; !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing seats mismatch:\n got %v\nwant %v", missing, want)
	}
}

func TestOrderCandidatesBySeat_NoSeatsIsIdentity(t *testing.T) {
	ids := testPeers(t, 4)
	cands := candidatesFor(t, ids)

	for name, seated := range map[string][]committee.Member{
		"nil":   nil,
		"empty": {},
	} {
		ordered, missing := OrderCandidatesBySeat(cands, seated)
		if missing != nil {
			t.Fatalf("%s: expected nil missing, got %v", name, missing)
		}
		if got, want := peerIDs(ordered), peerIDs(cands); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: order changed:\n got %v\nwant %v", name, got, want)
		}
	}
}

func TestOrderCandidatesBySeat_NoCandidatesReportsEverySeat(t *testing.T) {
	ids := testPeers(t, 2)
	seated := seatsFor(ids)

	ordered, missing := OrderCandidatesBySeat(nil, seated)
	if len(ordered) != 0 {
		t.Fatalf("expected empty ordering, got %d", len(ordered))
	}
	if want := []string{ids[0].String(), ids[1].String()}; !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing seats mismatch:\n got %v\nwant %v", missing, want)
	}
}

func TestOrderCandidatesBySeat_DuplicateCandidateConsumesOneSeat(t *testing.T) {
	ids := testPeers(t, 3)
	cands := candidatesFor(t, ids)
	// ids[1] appears twice in the pool.
	cands = append(cands, cands[1])

	seated := seatsFor([]peer.ID{ids[1], ids[2]})
	ordered, missing := OrderCandidatesBySeat(cands, seated)

	if len(missing) != 0 {
		t.Fatalf("expected no missing seats, got %v", missing)
	}
	assertPermutation(t, ordered, cands)
	want := []peer.ID{ids[1], ids[2], ids[0], ids[1]}
	if got := peerIDs(ordered); !reflect.DeepEqual(got, want) {
		t.Fatalf("order mismatch:\n got %v\nwant %v", got, want)
	}
}

// seedAddrsFor builds a peer_id -> []multiaddr map as ListBuddyMultiaddrs would
// return it, mirroring the deterministic candidatesFor addressing scheme.
func seedAddrsFor(ids []peer.ID) map[string][]string {
	out := make(map[string][]string, len(ids))
	for i, pid := range ids {
		out[pid.String()] = []string{
			fmt.Sprintf("/ip4/10.90.1.%d/tcp/15000/p2p/%s", i+1, pid.String()),
		}
	}
	return out
}

// The core of the fix: a seated peer the router never surfaced (absent from the
// pool) is added from the seed address book so it can be dialed and asked to
// vote. Existing candidates are untouched and kept in order.
func TestAddMissingSeatCandidates_AddsAbsentSeatFromSeed(t *testing.T) {
	ids := testPeers(t, 5)
	// Pool holds only the first two peers (what the router surfaced).
	cands := candidatesFor(t, ids[:2])
	// The draw seats two of those plus two the pool never had.
	dial := seatsFor([]peer.ID{ids[0], ids[3], ids[4]})
	seed := seedAddrsFor(ids)

	aug, added, unresolved := AddMissingSeatCandidates(cands, dial, seed)

	if len(unresolved) != 0 {
		t.Fatalf("expected all seats resolvable, got unresolved %v", unresolved)
	}
	if want := []string{ids[3].String(), ids[4].String()}; !reflect.DeepEqual(added, want) {
		t.Fatalf("added mismatch:\n got %v\nwant %v", added, want)
	}
	// Original pool preserved at the head, in order; new seats appended.
	wantOrder := []peer.ID{ids[0], ids[1], ids[3], ids[4]}
	if got := peerIDs(aug); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("augmented order mismatch:\n got %v\nwant %v", got, wantOrder)
	}
	// After the union, seat-ordering must find every seat (none missing).
	_, missing := OrderCandidatesBySeat(aug, dial)
	if len(missing) != 0 {
		t.Fatalf("after union no seat should be missing, got %v", missing)
	}
	// The added seats carry the seed address, not an empty one.
	for _, c := range aug {
		if c.PeerID == ids[3] || c.PeerID == ids[4] {
			if c.Multiaddr == nil {
				t.Fatalf("added seat %s has nil multiaddr", c.PeerID)
			}
		}
	}
}

// A seat with no address at the seed (and an unparseable one) is reported as
// unresolved rather than silently dropped, and is not added to the pool.
func TestAddMissingSeatCandidates_UnresolvedWhenNoSeedAddr(t *testing.T) {
	ids := testPeers(t, 4)
	cands := candidatesFor(t, ids[:1]) // pool has only ids[0]
	dial := seatsFor([]peer.ID{ids[0], ids[1], ids[2]})
	// Seed knows ids[1] but NOT ids[2]; also feed an unparseable seat.
	seed := map[string][]string{
		ids[1].String(): {"/ip4/10.90.1.2/tcp/15000/p2p/" + ids[1].String()},
	}
	dial = append(dial, committee.Member{PeerID: "not-a-peer-id"})

	aug, added, unresolved := AddMissingSeatCandidates(cands, dial, seed)

	if want := []string{ids[1].String()}; !reflect.DeepEqual(added, want) {
		t.Fatalf("added mismatch:\n got %v\nwant %v", added, want)
	}
	if want := []string{ids[2].String(), "not-a-peer-id"}; !reflect.DeepEqual(unresolved, want) {
		t.Fatalf("unresolved mismatch:\n got %v\nwant %v", unresolved, want)
	}
	// ids[2] must NOT have been added.
	for _, c := range aug {
		if c.PeerID == ids[2] {
			t.Fatalf("unresolved seat %s must not be added to the pool", ids[2])
		}
	}
	if got, want := len(aug), 2; got != want { // ids[0] (orig) + ids[1] (added)
		t.Fatalf("pool size: got %d, want %d", got, want)
	}
}

// A seat already in the pool is never duplicated, and the empty-dial case is a
// no-op that preserves the pool exactly.
func TestAddMissingSeatCandidates_NoDuplicatesAndEmptyDialIsIdentity(t *testing.T) {
	ids := testPeers(t, 3)
	cands := candidatesFor(t, ids)
	seed := seedAddrsFor(ids)

	// Every seat is already present → nothing added, order unchanged.
	aug, added, unresolved := AddMissingSeatCandidates(cands, seatsFor(ids), seed)
	if len(added) != 0 || len(unresolved) != 0 {
		t.Fatalf("expected no-op, got added=%v unresolved=%v", added, unresolved)
	}
	assertPermutation(t, aug, cands)
	if got, want := peerIDs(aug), peerIDs(cands); !reflect.DeepEqual(got, want) {
		t.Fatalf("order changed:\n got %v\nwant %v", got, want)
	}

	// Empty dial targets → identity.
	aug2, added2, unresolved2 := AddMissingSeatCandidates(cands, nil, seed)
	if len(added2) != 0 || len(unresolved2) != 0 {
		t.Fatalf("empty dial should be no-op, got added=%v unresolved=%v", added2, unresolved2)
	}
	if got, want := peerIDs(aug2), peerIDs(cands); !reflect.DeepEqual(got, want) {
		t.Fatalf("empty dial changed order:\n got %v\nwant %v", got, want)
	}
}

func TestReachableInCandidateOrder_FollowsCandidateOrder(t *testing.T) {
	ids := testPeers(t, 5)
	cands := candidatesFor(t, ids)

	// Reachable holds a different address than the candidate carried — the
	// dialled address must win.
	dialled, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/15000")
	if err != nil {
		t.Fatalf("multiaddr: %v", err)
	}
	reachable := map[peer.ID]multiaddr.Multiaddr{
		ids[4]: dialled,
		ids[1]: dialled,
		ids[2]: dialled,
	}

	got := ReachableInCandidateOrder(cands, reachable)

	want := []peer.ID{ids[1], ids[2], ids[4]}
	if g := peerIDs(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("order mismatch:\n got %v\nwant %v", g, want)
	}
	for _, c := range got {
		if !c.Multiaddr.Equal(dialled) {
			t.Fatalf("peer %s kept candidate address %s, want dialled %s",
				c.PeerID, c.Multiaddr, dialled)
		}
	}
}

func TestReachableInCandidateOrder_EmptyReachableIsEmpty(t *testing.T) {
	ids := testPeers(t, 3)
	cands := candidatesFor(t, ids)

	if got := ReachableInCandidateOrder(cands, nil); len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
	if got := ReachableInCandidateOrder(cands, map[peer.ID]multiaddr.Multiaddr{}); len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}

func TestReachableInCandidateOrder_NonCandidatesAppendedDeterministically(t *testing.T) {
	ids := testPeers(t, 6)
	cands := candidatesFor(t, ids[:3])
	ma, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/15000")
	if err != nil {
		t.Fatalf("multiaddr: %v", err)
	}
	reachable := map[peer.ID]multiaddr.Multiaddr{
		ids[0]: ma, ids[2]: ma, ids[3]: ma, ids[4]: ma, ids[5]: ma,
	}

	first := peerIDs(ReachableInCandidateOrder(cands, reachable))
	if len(first) != len(reachable) {
		t.Fatalf("dropped peers: got %d, want %d", len(first), len(reachable))
	}
	// Candidates first, in candidate order.
	if got, want := first[:2], []peer.ID{ids[0], ids[2]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate prefix mismatch:\n got %v\nwant %v", got, want)
	}
	// Non-candidates sorted by peer id.
	tail := append([]peer.ID(nil), first[2:]...)
	sorted := append([]peer.ID(nil), tail...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if !reflect.DeepEqual(tail, sorted) {
		t.Fatalf("tail not sorted: %v", tail)
	}
	// Same input, same output — 20 runs, map order varies per range.
	for i := 0; i < 20; i++ {
		if again := peerIDs(ReachableInCandidateOrder(cands, reachable)); !reflect.DeepEqual(again, first) {
			t.Fatalf("run %d differed:\n got %v\nwant %v", i, again, first)
		}
	}
}

// TestSeatOrderSurvivesTruncation is the regression test for the bug this file
// fixes: a pool larger than the connectedness probe's cap used to lose seated
// members to map-order truncation. With seat ordering, the first maxPeers
// entries always contain the whole seated committee.
func TestSeatOrderSurvivesTruncation(t *testing.T) {
	const (
		poolSize  = 20
		seatCount = 7 // config.MaxMainPeers
		maxPeers  = 12
	)
	ids := testPeers(t, poolSize)
	cands := candidatesFor(t, ids)

	// The seated committee is scattered across the pool, including the tail.
	seatIdx := []int{19, 2, 14, 0, 11, 18, 7}
	seatPeers := make([]peer.ID, 0, seatCount)
	for _, i := range seatIdx {
		seatPeers = append(seatPeers, ids[i])
	}
	seated := seatsFor(seatPeers)

	ordered, missing := OrderCandidatesBySeat(cands, seated)
	if len(missing) != 0 {
		t.Fatalf("expected no missing seats, got %v", missing)
	}

	// Everything is reachable, expressed as the map the probe actually gets.
	ma, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/15000")
	if err != nil {
		t.Fatalf("multiaddr: %v", err)
	}
	reachable := make(map[peer.ID]multiaddr.Multiaddr, poolSize)
	for _, pid := range ids {
		reachable[pid] = ma
	}

	probe := ReachableInCandidateOrder(ordered, reachable)
	if len(probe) != poolSize {
		t.Fatalf("probe list lost peers: got %d, want %d", len(probe), poolSize)
	}

	kept := make(map[peer.ID]struct{}, maxPeers)
	for _, c := range probe[:maxPeers] {
		kept[c.PeerID] = struct{}{}
	}
	for i, pid := range seatPeers {
		if _, ok := kept[pid]; !ok {
			t.Fatalf("seat %d (%s) fell outside the first %d probed peers", i, pid, maxPeers)
		}
	}
	// Tighter: the seated committee occupies exactly the head of the list.
	for i, pid := range seatPeers {
		if probe[i].PeerID != pid {
			t.Fatalf("probe position %d is %s, want seat %s", i, probe[i].PeerID, pid)
		}
	}
}
