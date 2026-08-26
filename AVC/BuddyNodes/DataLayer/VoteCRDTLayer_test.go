package DataLayer

// Stage 1 exit-criteria tests, per docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md §2.4:
//   - a node's VoteCRDTLayer is non-nil after construction
//   - it is a distinct engine instance from CRDTLayer (they must never alias
//     one another — Stage 3's §4.2 contamination risk depends on this)
//   - it starts empty, since nothing writes to it yet

import "testing"

func TestGetVoteCRDTLayer_ReturnsNonNilController(t *testing.T) {
	c := GetVoteCRDTLayer()
	if c == nil {
		t.Fatal("GetVoteCRDTLayer returned nil")
	}
	if c.CRDTLayer == nil {
		t.Fatal("GetVoteCRDTLayer returned a Controller with a nil engine")
	}
}

func TestGetVoteCRDTLayer_IsASingleton(t *testing.T) {
	a := GetVoteCRDTLayer()
	b := GetVoteCRDTLayer()
	if a != b {
		t.Fatal("GetVoteCRDTLayer returned two different instances across calls")
	}
}

// The vote CRDT and the existing jmdn CRDT must be genuinely separate
// engines. If they ever aliased the same underlying store, Stage 3's
// cross-layer contamination guard (LLD §4.2) would be silently defeated:
// old peer-keyed elements and new block-keyed elements would share one
// keyspace instead of two.
func TestVoteCRDTLayer_IsDistinctFromLegacyCRDTLayer(t *testing.T) {
	voteLayer := GetVoteCRDTLayer()
	legacyLayer := GetCRDTLayer()

	if voteLayer == nil || legacyLayer == nil {
		t.Fatal("both layers must be non-nil before comparing them")
	}
	// Comparing the engine pointers, not the Controller wrappers (which are
	// always distinct struct instances regardless of what they wrap).
	if any(voteLayer.CRDTLayer) == any(legacyLayer.CRDTLayer) {
		t.Fatal("VoteCRDTLayer and CRDTLayer point at the SAME underlying engine — they must be separate stores")
	}
}

func TestNewVoteCRDTLayer_NilEngineBuildsAFreshOne(t *testing.T) {
	c := NewVoteCRDTLayer(nil)
	if c == nil || c.CRDTLayer == nil {
		t.Fatal("NewVoteCRDTLayer(nil) must construct a usable engine, not leave it nil")
	}
}
