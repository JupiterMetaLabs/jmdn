package reputation

import "sync"

// Push-suspend guard (D-61). The reputation push is an ABSOLUTE overwrite of the
// seed's selection weights from this sequencer's in-memory store. When the
// sequencer's OWN recent rounds are failing (it is building on a block the fleet
// lacks, running a stale committee snapshot, or otherwise at fault), those
// failures show up as fleet-wide rejects/absents and depress every peer's score.
// Pushing that snapshot converts the sequencer's fault into fleet-wide selection
// weights — the feedback loop behind the 2026-09-17 halt. While the sequencer's
// recent round success rate is below quorum, the push must be SUSPENDED: it is
// the sequencer's fault, not the fleet's, and stale seed weights are safer than
// weights derived from a broken sequencer.

// RoundHealth is a fixed-window success/fail tracker over recent consensus rounds.
// Zero value is not usable; use NewRoundHealth.
type RoundHealth struct {
	mu     sync.Mutex
	window int
	ring   []bool // true = round reached consensus (success)
	n      int    // total recorded (caps at window)
	idx    int
}

// NewRoundHealth tracks the last `window` rounds (window <= 0 → 20).
func NewRoundHealth(window int) *RoundHealth {
	if window <= 0 {
		window = 20
	}
	return &RoundHealth{window: window, ring: make([]bool, window)}
}

// Record adds one round outcome.
func (h *RoundHealth) Record(success bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ring[h.idx] = success
	h.idx = (h.idx + 1) % h.window
	if h.n < h.window {
		h.n++
	}
}

// SuccessRate is the fraction of successful rounds in the window. With fewer than
// `minSamples` observations it returns 1.0 (assume healthy — do not suspend on a
// cold start, which would wrongly withhold the first legitimate pushes).
func (h *RoundHealth) SuccessRate(minSamples int) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.n < minSamples {
		return 1.0
	}
	ok := 0
	for i := 0; i < h.n; i++ {
		if h.ring[i] {
			ok++
		}
	}
	return float64(ok) / float64(h.n)
}

// ShouldSuspendPush reports whether the reputation push should be suspended:
// true when the recent success rate is strictly below `minRate`. minSamples
// guards the cold start. Default is the process-wide DefaultRoundHealth.
func (h *RoundHealth) ShouldSuspendPush(minRate float64, minSamples int) bool {
	return h.SuccessRate(minSamples) < minRate
}

// DefaultRoundHealth is the process-wide tracker the consensus loop records into
// and the push tick consults.
var DefaultRoundHealth = NewRoundHealth(20)
