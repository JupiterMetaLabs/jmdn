package thebesync

import (
	"errors"
	"testing"
)

func stubCertRule(t *testing.T, from uint64, parent func(uint64) (bool, error)) {
	t.Helper()
	prevFrom, prevParent := certificateRequiredFrom, parentHadCertificate
	certificateRequiredFrom = func() uint64 { return from }
	parentHadCertificate = parent
	lastAppliedMu.Lock()
	lastAppliedValid = false
	lastAppliedMu.Unlock()
	t.Cleanup(func() {
		certificateRequiredFrom, parentHadCertificate = prevFrom, prevParent
		lastAppliedMu.Lock()
		lastAppliedValid = false
		lastAppliedMu.Unlock()
	})
}

// The certificate requirement survives a new sync session: it is derived from
// the stored parent, not from in-session state.
func TestCertificateRequired_FromStoredParent(t *testing.T) {
	stubCertRule(t, 0, func(h uint64) (bool, error) { return h >= 100, nil })
	if certificateRequired(50, 49) {
		t.Fatal("legacy prefix block must not require a certificate")
	}
	if !certificateRequired(101, 100) {
		t.Fatal("child of a certified block must require a certificate on a fresh session")
	}
}

func TestCertificateRequired_ActivationHeight(t *testing.T) {
	stubCertRule(t, 1, func(uint64) (bool, error) { return false, nil })
	if !certificateRequired(1, 0) || !certificateRequired(5, 4) {
		t.Fatal("blocks at or above the activation height must require a certificate")
	}
}

func TestCertificateRequired_FailsClosedOnReadError(t *testing.T) {
	stubCertRule(t, 0, func(uint64) (bool, error) { return false, errors.New("db down") })
	if !certificateRequired(10, 9) {
		t.Fatal("unreadable parent must require a certificate")
	}
}

func TestCertificateRequired_UsesLastApplied(t *testing.T) {
	stubCertRule(t, 0, func(uint64) (bool, error) {
		t.Fatal("storage must not be read when the parent was just applied")
		return false, nil
	})
	noteApplied(20, true)
	if !certificateRequired(21, 20) {
		t.Fatal("child of a just-applied certified block must require a certificate")
	}
}
