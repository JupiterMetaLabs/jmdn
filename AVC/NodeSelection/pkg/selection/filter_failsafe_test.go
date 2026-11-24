package selection

import (
	"testing"

	seednodetypes "gossipnode/seednode/types"
)

// D-59 regression: when reputation weights push every active peer below the
// selection floor, the band filter empties the set — but the fail-safe must
// fall back to the active peers instead of returning nothing (which upstream
// turns into ErrNoPeersAvailable and a chain halt).

func mkNode(id string, active bool, sel float64) Node {
	return Node{
		Node:           seednodetypes.Node{PeerId: id, IsActive: active},
		SelectionScore: sel,
	}
}

func collapsedFleet(n int) []Node {
	nodes := make([]Node, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, mkNode("peer-"+string(rune('A'+i)), true, 0.34)) // below floor 0.5 — the incident's range
	}
	return nodes
}

func TestFilterEligible_EmptyWhenAllBelowFloor(t *testing.T) {
	got := FilterEligible("self", collapsedFleet(5), DefaultFilterConfig())
	if len(got) != 0 {
		t.Fatalf("precondition: expected band filter to drop all 5, got %d", len(got))
	}
}

func TestFilterEligibleOrActive_FallsBackToActiveSet(t *testing.T) {
	got, usedFallback := FilterEligibleOrActive("self", collapsedFleet(5), DefaultFilterConfig())
	if !usedFallback {
		t.Fatal("expected fail-safe to engage when the band emptied the set")
	}
	if len(got) != 5 {
		t.Fatalf("fail-safe should return all 5 active non-self peers, got %d", len(got))
	}
}

func TestFilterEligibleOrActive_ExcludesSelfAndInactiveEvenInFallback(t *testing.T) {
	nodes := collapsedFleet(3)
	nodes = append(nodes, mkNode("self", true, 0.34), mkNode("dead", false, 0.34))
	got, usedFallback := FilterEligibleOrActive("self", nodes, DefaultFilterConfig())
	if !usedFallback {
		t.Fatal("expected fallback")
	}
	if len(got) != 3 {
		t.Fatalf("fallback must still exclude self and inactive: want 3, got %d", len(got))
	}
	for _, n := range got {
		if n.PeerId == "self" || n.PeerId == "dead" {
			t.Fatalf("fallback leaked %s", n.PeerId)
		}
	}
}

func TestFilterEligibleOrActive_NoFallbackWhenBandNonEmpty(t *testing.T) {
	nodes := collapsedFleet(2)
	nodes = append(nodes, mkNode("healthy", true, 0.80))
	got, usedFallback := FilterEligibleOrActive("self", nodes, DefaultFilterConfig())
	if usedFallback {
		t.Fatal("must NOT fall back while a banded candidate exists")
	}
	if len(got) != 1 || got[0].PeerId != "healthy" {
		t.Fatalf("expected only the healthy peer, got %+v", got)
	}
}

func TestFilterEligibleOrActive_TrulyEmptyStaysEmpty(t *testing.T) {
	// no active non-self peers → genuinely nothing to select; must NOT fabricate one
	nodes := []Node{mkNode("self", true, 0.8)}
	got, usedFallback := FilterEligibleOrActive("self", nodes, DefaultFilterConfig())
	if usedFallback || len(got) != 0 {
		t.Fatalf("empty fleet must return empty, got %d fallback=%v", len(got), usedFallback)
	}
}
