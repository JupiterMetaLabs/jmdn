package messaging

// Cross-process verification for the committee/seed console logging added to
// RoundContextForBlock and SelectCommitteeWithSize (messaging/committee_v2.go).
//
// Same harness shape as committee_agreement_test.go's
// TestCommitteeAgreementAcrossProcesses, and for the same reason: only a
// separate OS process (its own address space, its own map iteration order)
// can prove nothing node-local leaked into what gets logged. This file
// proves two things empirically, not by inspection:
//
//  1. The new Info-level "round context built" / "buddy committee selected"
//     log lines actually reach a real process's console output with zero
//     configuration — they log through the zerolog global logger
//     (github.com/rs/zerolog/log), the same one consensus_hardening.go
//     already uses in this package, whose default level has nothing
//     restricting it (no SetGlobalLevel call anywhere in this codebase).
//  2. Independent processes given the same round produce byte-identical
//     slot / period / entropy_epoch / selection_period / seed /
//     entropy_sha256 / committee member list — the exact four-way
//     agreement ("same slot, same entropy, same entropy committee, same
//     buddy committee") the logging exists to let an operator check.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"

	"gossipnode/config"
)

const (
	childLogEnvKey    = "JMDN_COMMITTEE_LOG_CHILD"
	childLogHeightKey = "JMDN_COMMITTEE_LOG_HEIGHT"
	childLogSlotKey   = "JMDN_COMMITTEE_LOG_SLOT"
	childLogPoolKey   = "JMDN_COMMITTEE_LOG_POOL"
	childLogSeatsKey  = "JMDN_COMMITTEE_LOG_SEATS"
)

// TestChildLogCommitteeSelection is the child-process entry point. Inert in
// a normal run; only does work when the parent sets childLogEnvKey.
func TestChildLogCommitteeSelection(t *testing.T) {
	if os.Getenv(childLogEnvKey) == "" {
		t.Skip("child-process entry point; driven by TestCommitteeLoggingAgreesAcrossProcesses")
	}

	height, err := strconv.ParseUint(os.Getenv(childLogHeightKey), 10, 64)
	if err != nil {
		t.Fatalf("child: bad height: %v", err)
	}
	slot, err := strconv.ParseUint(os.Getenv(childLogSlotKey), 10, 64)
	if err != nil {
		t.Fatalf("child: bad slot: %v", err)
	}
	pool, err := strconv.Atoi(os.Getenv(childLogPoolKey))
	if err != nil {
		t.Fatalf("child: bad pool: %v", err)
	}
	seats, err := strconv.Atoi(os.Getenv(childLogSeatsKey))
	if err != nil {
		t.Fatalf("child: bad seats: %v", err)
	}

	// No log-level setup needed: committee_v2.go logs through the same
	// zerolog global logger (github.com/rs/zerolog/log) consensus_hardening.go
	// already uses in this package, whose global default level is
	// unrestricted (TraceLevel) unless something calls SetGlobalLevel -
	// nothing in this codebase does - so Info reaches console output with
	// zero configuration, matching how every other log.Info()/log.Warn()
	// call in this package already behaves.
	wireEligibility(t, pool)

	block := &config.ZKBlock{BlockNumber: height, Slot: slot}
	rc, err := RoundContextForBlock(block)
	if err != nil {
		t.Fatalf("child: RoundContextForBlock: %v", err)
	}

	members, err := SelectCommitteeWithSize(rc, seats)
	if err != nil {
		t.Fatalf("child: SelectCommitteeWithSize: %v", err)
	}

	// Independently recompute seed + entropy hash here, the same way the
	// production log line does, purely to emit an unambiguous marker line
	// the parent can diff byte-for-byte without parsing the logger's own
	// (format-configurable) console encoding.
	seedSrc, err := SeedSourceFor(rc.EntropyEpoch)
	if err != nil {
		t.Fatalf("child: SeedSourceFor: %v", err)
	}
	entropy, err := seedSrc.EpochEntropy(rc.EntropyEpoch)
	if err != nil {
		t.Fatalf("child: EpochEntropy: %v", err)
	}
	seed, err := committee.DeriveSeed(seedSrc, committee.SeedInput{
		EntropyEpoch: rc.EntropyEpoch,
		PrevHash:     rc.PrevHash,
		Height:       rc.Height,
		Period:       rc.Period,
	})
	if err != nil {
		t.Fatalf("child: DeriveSeed: %v", err)
	}

	fmt.Printf("MARK|slot=%d|period=%d|entropy_epoch=%d|selection_period=%d|seed=%s|entropy_sha256=%x|members=%s\n",
		block.Slot, rc.Period, uint64(rc.EntropyEpoch), uint64(rc.SelectionPeriod),
		seed.String(), sha256.Sum256(entropy), strings.Join(ids(members), ","))
}

// TestCommitteeLoggingAgreesAcrossProcesses spawns independent OS processes
// with the SAME round and pool and requires:
//   - each one's real console output actually contains the new log lines
//     (proves the logging fires, at the level a live node would use it at)
//   - every process's slot/period/entropy_epoch/selection_period/seed/
//     entropy_sha256/committee list is byte-identical (proves the "same
//     slot, same entropy, same entropy-derived seed, same buddy committee"
//     property the logging exists to make checkable)
func TestCommitteeLoggingAgreesAcrossProcesses(t *testing.T) {
	const nodes, height, slot, pool, seats = 3, 12_345, 777, 20, testSeats

	var marks []string
	for i := 0; i < nodes; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestChildLogCommitteeSelection$", "-test.v")
		cmd.Env = append(os.Environ(),
			childLogEnvKey+"=1",
			childLogHeightKey+"="+strconv.FormatUint(uint64(height), 10),
			childLogSlotKey+"="+strconv.FormatUint(uint64(slot), 10),
			childLogPoolKey+"="+strconv.Itoa(pool),
			childLogSeatsKey+"="+strconv.Itoa(seats),
		)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child %d failed: %v\n%s", i, err, raw)
		}
		out := string(raw)

		if !strings.Contains(out, "round context built") {
			t.Fatalf("child %d: console output does not contain the \"round context built\" log line "+
				"(logging did not fire at info level)\n%s", i, out)
		}
		if !strings.Contains(out, "buddy committee selected") {
			t.Fatalf("child %d: console output does not contain the \"buddy committee selected\" log line "+
				"(logging did not fire at info level)\n%s", i, out)
		}

		var mark string
		for _, line := range strings.Split(out, "\n") {
			if after, ok := strings.CutPrefix(strings.TrimSpace(line), "MARK|"); ok {
				mark = after
				break
			}
		}
		if mark == "" {
			t.Fatalf("child %d produced no MARK line\n%s", i, out)
		}
		marks = append(marks, mark)
	}

	for i := 1; i < len(marks); i++ {
		if marks[i] != marks[0] {
			t.Fatalf("process 0 and process %d disagree:\n  0: %s\n  %d: %s", i, marks[0], i, marks[i])
		}
	}
	t.Logf("%d independent processes, height=%d slot=%d pool=%d -> identical slot/entropy/seed/committee: %s",
		nodes, height, slot, pool, marks[0])
}
