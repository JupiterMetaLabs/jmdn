package DataLayer

// Stage 1 of docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md: stand up the avc CRDT
// engine the vote path will move onto, alongside the existing jmdn engine in
// CRDTLayer.go. Same singleton shape as GetCRDTLayer there (sync.Once +
// package var), deliberately, so the vote path's construction is
// unsurprising to anyone who has read that file.
//
// Nothing in this repository writes to or reads from this controller yet.
// That is Stage 1's whole point — infrastructure with no behavioural change,
// so it is safe to merge and safe to leave in place indefinitely while later
// stages land. CRDTLayer.go's engine remains the sole vote store through
// Stage 6; do not point any live code at this one before then.

import (
	"sync"

	avccrdt "github.com/JupiterMetaLabs/avc/crdt"
	avctypes "github.com/JupiterMetaLabs/avc/types"
)

// voteCRDTMaxHeapBytes matches CRDTLayer.go's NewEngineMemOnly budget
// (50MB) exactly. Stages 2-6 hold vote data in both engines at once, so vote
// CRDT memory is roughly double during the migration — see the LLD's §2.3
// and §11. Not a reason to under-size this one; under-sizing it just moves
// the pressure to whichever engine fills first.
const voteCRDTMaxHeapBytes = 1024 * 1024 * 50

var (
	voteCRDTOnce sync.Once
	voteCRDT     *avctypes.Controller
)

// NewVoteCRDTLayer wraps the given avc engine in a Controller, or builds a
// fresh memory-only one if engine is nil. Mirrors NewCRDTLayer above exactly.
func NewVoteCRDTLayer(engine *avccrdt.Engine) *avctypes.Controller {
	if engine == nil {
		engine = avccrdt.NewEngineMemOnly(voteCRDTMaxHeapBytes)
	}
	return &avctypes.Controller{CRDTLayer: engine}
}

// GetVoteCRDTLayer returns the process-wide singleton vote CRDT controller,
// constructing it on first call. Call this once at node startup (see
// node/node.go) and store the result on the listener BuddyNode via
// SetVoteCRDTLayer / the VoteCRDTLayer field — do not call this repeatedly
// from the hot path, the same discipline GetCRDTLayer already follows.
func GetVoteCRDTLayer() *avctypes.Controller {
	voteCRDTOnce.Do(func() {
		voteCRDT = NewVoteCRDTLayer(nil)
	})
	return voteCRDT
}
