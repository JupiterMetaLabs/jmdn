package messaging

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/randao"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

func signedReveal(t *testing.T, epoch uint64) (string, []byte) {
	t.Helper()
	priv, id := newTestIdentity(t)
	sig, err := randao.SignReveal(priv, BLS_Signer.DomainChainID(), epoch, id)
	if err != nil {
		t.Fatal(err)
	}
	return id, sig
}

func withRevealClock(t *testing.T, epoch uint64, known bool) {
	t.Helper()
	prev := revealCurrentEpoch
	revealCurrentEpoch = func() (uint64, bool) { return epoch, known }
	t.Cleanup(func() { revealCurrentEpoch = prev })
}

func withSeated(t *testing.T, fn func(uint64, string) bool) {
	t.Helper()
	prev := revealPeerSeated
	revealPeerSeated = fn
	t.Cleanup(func() { revealPeerSeated = prev })
}

func TestAddInboundReveal_EpochWindow(t *testing.T) {
	resetRevealInbox(t)
	withRevealClock(t, 10, true)

	for _, tc := range []struct {
		epoch uint64
		ok    bool
	}{{9, true}, {10, true}, {12, true}, {13, false}, {8, false}, {1 << 40, false}} {
		id, sig := signedReveal(t, tc.epoch)
		err := AddInboundReveal(tc.epoch, id, sig)
		if tc.ok && err != nil {
			t.Errorf("epoch %d rejected: %v", tc.epoch, err)
		}
		if !tc.ok && !errors.Is(err, ErrRevealEpochOutOfWindow) {
			t.Errorf("epoch %d: err = %v, want out-of-window", tc.epoch, err)
		}
	}
}

func TestAddInboundReveal_NoClockNoWindow(t *testing.T) {
	resetRevealInbox(t)
	withRevealClock(t, 0, false)
	id, sig := signedReveal(t, 1<<20)
	if err := AddInboundReveal(1<<20, id, sig); err != nil {
		t.Fatalf("before the slot clock starts no window applies: %v", err)
	}
}

func TestAddInboundReveal_EpochCountCap(t *testing.T) {
	resetRevealInbox(t)
	withRevealClock(t, 0, false)
	for e := uint64(0); e < maxRevealInboxEpochs; e++ {
		id, sig := signedReveal(t, e)
		if err := AddInboundReveal(e, id, sig); err != nil {
			t.Fatal(err)
		}
	}
	id, sig := signedReveal(t, 999)
	if err := AddInboundReveal(999, id, sig); !errors.Is(err, ErrRevealInboxFull) {
		t.Fatalf("err = %v, want inbox full", err)
	}
}

// At capacity only committee members get in, so self-signed reveals from
// non-members cannot crowd them out.
func TestAddInboundReveal_PerEpochCapAdmitsMembers(t *testing.T) {
	resetRevealInbox(t)
	withRevealClock(t, 0, false)
	member, memberSig := signedReveal(t, 3)
	withSeated(t, func(_ uint64, p string) bool { return p == member })

	for i := 0; i < maxRevealsPerEpoch; i++ {
		id, sig := signedReveal(t, 3)
		if err := AddInboundReveal(3, id, sig); err != nil {
			t.Fatal(err)
		}
	}
	id, sig := signedReveal(t, 3)
	if err := AddInboundReveal(3, id, sig); !errors.Is(err, ErrRevealInboxFull) {
		t.Fatalf("non-member at capacity: err = %v, want inbox full", err)
	}
	if err := AddInboundReveal(3, member, memberSig); err != nil {
		t.Fatalf("member at capacity rejected: %v", err)
	}
}
