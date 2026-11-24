package messaging

// D-37 — RecoverAggSigStoreAtStartup must refuse when the committee eligibility
// source is not wired, instead of reporting a clean zero.
//
// WHAT SHIPPED. The function was called from main() ~400 lines before
// SetCommitteeEligibilitySource / Sequencer.WireCommitteeSources, both of which
// need n.Host and therefore cannot run before node.NewNode(). So on every real
// node:
//
//	VerifyAndRecordPrevCert -> verifyCertAndAggregate -> committeeSnapshotFor
//	  -> eligibleMembersUncappedForEpoch -> "eligibility source not configured"
//
// for EVERY replayed block. `recovered` was therefore always 0, and because the
// call site branched only on `rerr != nil` and `recovered > 0`, NEITHER branch
// printed anything. The sole visible symptom was up to maxRecoveryScanBlocks
// log.Error lines reading "block's parent certificate failed verification",
// which reads as tampering rather than a wiring bug.
//
// The restart divergence the function exists to fix therefore stayed unfixed on
// every node, silently. These tests make the precondition self-enforcing: a
// zero that means "not wired" must never again be indistinguishable from a zero
// that means "the window has only just opened".

import (
	"strings"
	"testing"
)

// THE REGRESSION TEST.
func TestRecoveryRefusesWhenEligibilitySourceIsUnwired(t *testing.T) {
	ResetAggSigStoreForTest()
	t.Cleanup(ResetAggSigStoreForTest)

	origFlag := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = origFlag })

	// Unwire the source, exactly as main() had it at the old call position.
	SetCommitteeEligibilitySource(nil)
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })

	const tip = 408 // epoch 8, window [403, 410) — a tip INSIDE the window
	chain := buildChain(tip)

	recovered, err := RecoverAggSigStoreAtStartup(tip, tip, chain.get)

	if err == nil {
		t.Fatalf("D-37 REGRESSION: recovery reported success (recovered=%d) with NO eligibility "+
			"source wired. Every replayed certificate fails verification in that state, so this "+
			"rebuild recovers nothing while looking like it worked — which is exactly how the "+
			"defect stayed invisible in production.", recovered)
	}
	if recovered != 0 {
		t.Errorf("recovered=%d alongside an error; a refusal must record nothing", recovered)
	}
	// The error has to name the fix, or it is just a different silent failure.
	if !strings.Contains(err.Error(), "SetCommitteeEligibilitySource") {
		t.Errorf("the refusal must name the wiring call an operator has to move; got: %v", err)
	}
	// It must refuse UP FRONT, not after walking the whole window.
	if chain.reads > 1 {
		t.Errorf("read %d blocks before refusing — the probe should happen once, before the walk, "+
			"not per block (that is what produced the misleading error spam)", chain.reads)
	}
}

// With the source wired, the same call must proceed — the guard must gate on
// wiring, not become a blanket refusal.
func TestRecoveryProceedsWhenEligibilitySourceIsWired(t *testing.T) {
	ResetAggSigStoreForTest()
	t.Cleanup(ResetAggSigStoreForTest)

	origFlag := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = origFlag })

	// TestMain installs defaultTestEligibility; assert rather than assume.
	SetCommitteeEligibilitySource(defaultTestEligibility)

	const tip = 408
	chain := buildChain(tip)

	if _, err := RecoverAggSigStoreAtStartup(tip, tip, chain.get); err != nil {
		t.Fatalf("recovery must proceed with a wired eligibility source: %v", err)
	}
	if chain.reads < 2 {
		t.Errorf("read only %d block(s) — the walk did not traverse the window, so this test "+
			"does not actually prove the guard lets a wired node through", chain.reads)
	}
}

// Genesis stays a clean no-op regardless of wiring: with no committed blocks
// there is nothing the source could have been needed for.
func TestRecoveryGenesisNoOpDoesNotRequireEligibilitySource(t *testing.T) {
	origFlag := AggCertEnabled
	AggCertEnabled = true
	t.Cleanup(func() { AggCertEnabled = origFlag })

	SetCommitteeEligibilitySource(nil)
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })

	chain := buildChain(0)
	n, err := RecoverAggSigStoreAtStartup(0, 0, chain.get)
	if err != nil {
		t.Fatalf("genesis must remain a clean no-op even with no source wired: %v", err)
	}
	if n != 0 || chain.reads != 0 {
		t.Fatalf("recovered=%d reads=%d, want 0/0 at genesis", n, chain.reads)
	}
}

// The disabled path must short-circuit before the guard, so turning the feature
// off cannot start failing startup.
func TestRecoveryDisabledPathIgnoresEligibilityWiring(t *testing.T) {
	origFlag := AggCertEnabled
	AggCertEnabled = false
	t.Cleanup(func() { AggCertEnabled = origFlag })

	SetCommitteeEligibilitySource(nil)
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })

	chain := buildChain(100)
	n, err := RecoverAggSigStoreAtStartup(60, 100, chain.get)
	if err != nil {
		t.Fatalf("with AggCertEnabled off this must be a silent no-op, not a refusal: %v", err)
	}
	if n != 0 || chain.reads != 0 {
		t.Fatalf("recovered=%d reads=%d, want 0/0 with the flag off", n, chain.reads)
	}
}
