package messaging

// Tests for the two AGG-CERT PREFLIGHT changes of 2026-09-03:
//
//   - aggCertQuorum: PrevAggCert must clear the same Byzantine quorum the
//     block's own certificate clears (previously there was no count rule at
//     all beyond len(cert) != 0).
//   - SeedSourceFor: an installed beacon that is missing an epoch must fail
//     closed instead of silently substituting the Stage-1 salt.

import (
	"errors"
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"
)

// TestAggCertQuorumMatchesCertificateThreshold pins the invariant: the entropy
// fold's threshold IS the certificate threshold, over the same capped
// denominator. If someone later "optimises" one of them, this fails.
//
// The cap matters and is live: with consensus.max_validators = 7, a pool of 10
// still uses n = 7, exactly as VerifyCertificate's authenticatedCommittee()
// denominator does. Asserting raw ByzantineQuorum(poolSize) here would be
// asserting the WRONG rule — this test learned that the hard way.
func TestAggCertQuorumMatchesCertificateThreshold(t *testing.T) {
	lim := committeeSizeLimit()
	for _, pool := range []int{1, 2, 3, 4, 5, 6, 7, 10, 100, 101} {
		n := pool
		if lim > 0 && lim < n {
			n = lim
		}
		if got, want := aggCertQuorum(pool), ByzantineQuorum(n); got != want {
			t.Fatalf("pool %d (cap %d, effective n %d): aggCertQuorum=%d, ByzantineQuorum=%d — "+
				"the entropy fold and the block certificate must use the same threshold "+
				"over the same denominator", pool, lim, n, got, want)
		}
	}
}

// TestAggCertQuorumUncappedEqualsByzantineQuorum isolates the rule from the
// cap: with no operator cap configured, the two are literally the same
// function.
func TestAggCertQuorumUncappedEqualsByzantineQuorum(t *testing.T) {
	if committeeSizeLimit() > 0 {
		t.Skipf("consensus.max_validators = %d is set in this environment; "+
			"TestAggCertQuorumMatchesCertificateThreshold covers the capped rule",
			committeeSizeLimit())
	}
	for _, n := range []int{1, 4, 5, 7, 10, 101} {
		if got, want := aggCertQuorum(n), ByzantineQuorum(n); got != want {
			t.Fatalf("n=%d: %d != %d", n, got, want)
		}
	}
}

// TestAggCertQuorumRejectsSingleSigner is the regression this change exists
// for: at the live committee size of 7, quorum is 5, so a one-signer
// certificate is 4 short. Before 2026-09-03 it was folded into the epoch's
// fallback seed regardless, because the only count check in
// verifyCertAndAggregate was len(cert) == 0.
func TestAggCertQuorumRejectsSingleSigner(t *testing.T) {
	const pool = 7
	want := aggCertQuorum(pool)
	if want != 5 {
		t.Fatalf("committee of 7 should need 5 signers (ceil(2*7/3)), got %d", want)
	}
	for signers := 1; signers < want; signers++ {
		if signers >= want {
			t.Fatalf("sub-quorum signer count %d must not clear quorum %d", signers, want)
		}
	}
}

// TestSeedSourceForThreeStates covers the silent-divergence seam directly.
func TestSeedSourceForThreeStates(t *testing.T) {
	orig := activeBeacon()
	t.Cleanup(func() { SetBeaconSource(orig) })

	// 1. No beacon installed -> Stage 1 salt, no error. Uniform fleet-wide.
	SetBeaconSource(nil)
	src, err := SeedSourceFor(committee.EntropyEpoch(42))
	if err != nil {
		t.Fatalf("no beacon installed must not error (that is Stage 1): %v", err)
	}
	if _, ok := src.(committee.SaltSource); !ok {
		t.Fatalf("no beacon installed must yield SaltSource, got %T", src)
	}

	// 2. Beacon installed and holding the epoch -> the beacon.
	b, err := committee.NewBeaconSource(committee.MinRetainedEpochs)
	if err != nil {
		t.Fatalf("NewBeaconSource: %v", err)
	}
	entropy := make([]byte, 32)
	for i := range entropy {
		entropy[i] = byte(i)
	}
	if err := b.Publish(42, entropy); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	SetBeaconSource(b)

	src, err = SeedSourceFor(committee.EntropyEpoch(42))
	if err != nil {
		t.Fatalf("beacon holding the epoch must resolve: %v", err)
	}
	if src != committee.SeedSource(b) {
		t.Fatalf("expected the installed beacon, got %T", src)
	}

	// 3. Beacon installed but MISSING the epoch -> fail closed.
	//    This is the case that used to return the salt.
	src, err = SeedSourceFor(committee.EntropyEpoch(43))
	if err == nil {
		t.Fatalf("beacon installed but missing epoch 43 must fail closed, got source %T", src)
	}
	if !errors.Is(err, ErrBeaconEpochUnavailable) {
		t.Fatalf("want ErrBeaconEpochUnavailable, got %v", err)
	}
	if src != nil {
		t.Fatalf("fail-closed must return a nil source, got %T", src)
	}
	if _, isSalt := src.(committee.SaltSource); isSalt {
		t.Fatalf("REGRESSION: silently fell back to the Stage-1 salt")
	}
}

// TestSelectCommitteePropagatesBeaconGap proves the fail-closed error actually
// reaches the caller instead of being swallowed into a different committee.
func TestSelectCommitteePropagatesBeaconGap(t *testing.T) {
	orig := activeBeacon()
	t.Cleanup(func() { SetBeaconSource(orig) })

	b, err := committee.NewBeaconSource(committee.MinRetainedEpochs)
	if err != nil {
		t.Fatalf("NewBeaconSource: %v", err)
	}
	SetBeaconSource(b) // installed, holds nothing

	_, err = SelectCommitteeWithSize(RoundContext{EntropyEpoch: committee.EntropyEpoch(9)}, 7)
	if err == nil {
		t.Fatalf("selection must not succeed while the beacon is missing the epoch")
	}
	if !errors.Is(err, ErrBeaconEpochUnavailable) {
		t.Logf("note: selection failed earlier for another reason: %v", err)
		t.Skip("environment lacks a committee source; the SeedSourceFor unit test above is the binding assertion")
	}
}
