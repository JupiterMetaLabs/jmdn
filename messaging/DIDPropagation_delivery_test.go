package messaging

// Tests for the DID-propagation delivery reporting added after block 13756 was
// rejected 0-of-7 with "receiver account … not found in cache".
//
// The failure was not a crypto or consensus bug. The account existed on the
// sequencer and nowhere else, and PropagateDID reported success anyway: it
// returned nil when there were no connected peers, and returned nil when every
// send failed, having computed a success count it then discarded. Meanwhile the
// whole subsystem's logging was being thrown away by a global-logger override in
// another package, so none of it was visible.
//
// These tests lock the reporting behaviour. They do not spin up libp2p hosts —
// the delivery accounting is the part that was wrong, and it is pure logic.
// The last test, on the receive side, does: a received DID message must not be
// re-forwarded.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/internal/saferun"
)

// withCommittee swaps the package-global eligibility source for one test.
//
// Cleanup restores defaultTestEligibility, NOT nil: the source is process-global
// and the rest of this package's tests (blockPropagation_test.go,
// cache_after_validation_test.go) rely on it being wired. Clearing it to nil
// makes every later certificate check fail with committee_source_invalid, which
// looks like a production regression and is not one.
func withCommittee(t *testing.T, members map[string]string, err error) {
	t.Helper()
	SetCommitteeEligibilitySource(func(_ uint64, _ bool) (map[string]string, error) {
		return members, err
	})
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
}

func peers(ids ...string) []peer.ID {
	out := make([]peer.ID, 0, len(ids))
	for _, id := range ids {
		out = append(out, peer.ID(id))
	}
	return out
}

// committeeOf builds an eligibility map keyed the way the real one is: by
// peer.ID.String(), which base58-encodes the ID rather than returning the raw
// bytes. Keying by the raw string silently matches nothing.
func committeeOf(ids ...string) map[string]string {
	m := make(map[string]string, len(ids))
	for _, id := range ids {
		m[peer.ID(id).String()] = ""
	}
	return m
}

// No eligibility source (every non-sequencer node) MUST fail open: an error is
// returned so the caller keeps propagating instead of refusing to.
func TestCommitteeDeliveryStatusFailsOpenWithoutSource(t *testing.T) {
	SetCommitteeEligibilitySource(nil)
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })

	reached, size, err := committeeDeliveryStatus(peers("a", "b"), 2)
	if err == nil {
		t.Fatal("expected an error when no committee source is wired — callers must fail OPEN")
	}
	if reached != 0 || size != 0 {
		t.Fatalf("reached/size = %d/%d, want 0/0", reached, size)
	}
}

// A source error must be reported, not silently treated as an empty committee
// (which would make the quorum check pass vacuously).
func TestCommitteeDeliveryStatusPropagatesSourceError(t *testing.T) {
	withCommittee(t, nil, fmt.Errorf("seed unreachable"))
	if _, _, err := committeeDeliveryStatus(peers("a"), 1); err == nil {
		t.Fatal("expected the source error to propagate")
	}
}

func TestCommitteeDeliveryStatusCountsOnlyCommitteeTargets(t *testing.T) {
	withCommittee(t, committeeOf("c1", "c2", "c3"), nil)

	// Two of the four targets are committee members, and all four sends worked.
	reached, size, err := committeeDeliveryStatus(peers("x", "c1", "y", "c2"), 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 3 {
		t.Fatalf("committee size = %d, want 3", size)
	}
	if reached != 2 {
		t.Fatalf("reached = %d, want 2 (non-committee peers must not count)", reached)
	}
}

// Delivery is unattributable per peer, so a success count lower than the number
// of committee targets must be assumed to be the worst case.
func TestCommitteeDeliveryStatusIsConservative(t *testing.T) {
	withCommittee(t, committeeOf("c1", "c2", "c3"), nil)

	// Three committee members targeted, but only one send succeeded overall.
	reached, _, err := committeeDeliveryStatus(peers("c1", "c2", "c3"), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reached != 1 {
		t.Fatalf("reached = %d, want 1 — must not claim more than actually delivered", reached)
	}
}

// The quorum arithmetic PropagateDID gates on. A committee of 7 needs 5; block
// 13756's committee was exactly 7 and reached 0.
func TestCommitteeQuorumGate(t *testing.T) {
	for _, tc := range []struct {
		size, reached int
		wantBlocked   bool
	}{
		{7, 0, true},  // the observed incident
		{7, 4, true},  // one short of 2f+1
		{7, 5, false}, // exactly quorum
		{7, 7, false},
		{1, 1, false},
	} {
		need := ByzantineQuorum(tc.size)
		blocked := tc.reached < need
		if blocked != tc.wantBlocked {
			t.Fatalf("committee %d reached %d (need %d): blocked=%v, want %v",
				tc.size, tc.reached, need, blocked, tc.wantBlocked)
		}
	}
	if got := ByzantineQuorum(7); got != 5 {
		t.Fatalf("ByzantineQuorum(7) = %d, want 5", got)
	}
}

// Regression guard for the root cause of the outage's invisibility.
//
// messaging/directMSG had, in an init(), `log.Logger = log.Output(...io.Discard)`.
// That reassigns zerolog's PROCESS-WIDE logger, so every global-zerolog line in
// the binary vanished — including all of DIDPropagation.go's success and failure
// logging. A package must silence its OWN logger, never the global one.
//
// This is a source-level check on purpose: the damage is done by a package init
// at load time, so by the time any test runs the global logger is already
// clobbered and there is nothing left to observe.
func TestNoPackageDiscardsTheGlobalZerologLogger(t *testing.T) {
	root := ".."
	var offenders []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable paths are not this test's business
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // the explanatory comment left at the fix site
			}
			// Assigning zerolog's package-level global logger.
			if strings.Contains(trimmed, "log.Logger =") {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", path, i+1, trimmed))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Fatalf("a package reassigns zerolog's GLOBAL logger, which silences or "+
			"redirects logging for the whole binary (this is how DID propagation "+
			"failures became invisible). Give the package its own logger instead:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// Any peer could send a DID message, unsigned and with a sender-chosen hop
// count, and every node stored it and re-forwarded it to every peer, so one
// message from one connection flooded the network. A received DID message is
// no longer re-forwarded.
func TestHandleDIDStream_ReceivedMessageIsNotReforwarded(t *testing.T) {
	attacker, receiver, neighbour := newLoopbackHost(t), newLoopbackHost(t), newLoopbackHost(t)
	receiver.SetStreamHandler(config.DIDPropagationProtocol, saferun.Stream(string(config.DIDPropagationProtocol), HandleDIDStream))
	prevHost := getHostInstance()
	SetHostInstance(receiver)
	t.Cleanup(func() { SetHostInstance(prevHost) })

	var forwarded int32
	neighbour.SetStreamHandler(config.DIDPropagationProtocol, func(s network.Stream) {
		atomic.AddInt32(&forwarded, 1)
		_ = s.Close()
	})
	connect(t, receiver, neighbour)
	connect(t, attacker, receiver)

	addr := common.HexToAddress("0x00000000000000000000000000000000000d1d01")
	frame, err := json.Marshal(DIDMessage{
		Sender: attacker.ID().String(), Timestamp: time.Now().Unix(), Type: "did_created",
		Hops: 0, Account: &DB_OPs.Account{Address: addr, DIDAddress: "did:jmdt:spam"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := attacker.NewStream(context.Background(), receiver.ID(), config.DIDPropagationProtocol)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write(append(frame, '\n'))
	_ = s.Close()

	// Wait until the receiver has handled the message, then give any forward
	// time to reach the neighbour before asserting it did not.
	deadline := time.Now().Add(5 * time.Second)
	for !isAccountMessageProcessed(deriveMessageID(addr)) {
		if time.Now().After(deadline) {
			t.Fatal("the receiver never handled the DID message")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if n := atomic.LoadInt32(&forwarded); n != 0 {
		t.Fatalf("a received DID message was re-forwarded to %d peer stream(s); want 0", n)
	}
}
