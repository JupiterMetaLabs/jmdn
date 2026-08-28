package Sequencer

// Tests for VDFSealer, covering AVC-Low-Level-Design.md §1's "Tests that
// matter": the goroutine launch must not block the caller (so it never stalls
// block commits), a node that misses the deadline must fail closed rather
// than guess, and two independent seals of the same input must agree
// (VDF determinism - the property the whole "all nodes compute independently"
// design rests on).

import (
	"math/big"
	"testing"
	"time"

	"github.com/JupiterMetaLabs/avc/beacon"
	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/JupiterMetaLabs/avc/vdf"
)

const (
	testChain      = 7000700
	testEpoch      = 421
	testDifficulty = 3000 // small so tests stay fast; production calibrates for seconds
)

// testGroup builds a small RSA group. TEST ONLY - the factors are derivable
// from this source, so it has a public trapdoor and provides no security.
func testGroup(t testing.TB) vdf.Group {
	t.Helper()
	p := nextPrime(new(big.Int).Lsh(big.NewInt(1), 1024))
	q := nextPrime(new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 1024), big.NewInt(1<<20)))
	g, err := vdf.NewRSAGroup(new(big.Int).Mul(p, q), "test-semiprime")
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func nextPrime(n *big.Int) *big.Int {
	c := new(big.Int).Set(n)
	if c.Bit(0) == 0 {
		c.Add(c, big.NewInt(1))
	}
	for !c.ProbablyPrime(20) {
		c.Add(c, big.NewInt(2))
	}
	return c
}

func testPipeline(t *testing.T) (*beacon.Pipeline, vdf.Group) {
	t.Helper()
	group := testGroup(t)
	sink, err := committee.NewBeaconSource(committee.MinRetainedEpochs)
	if err != nil {
		t.Fatal(err)
	}
	p, err := beacon.New(group, testDifficulty, sink)
	if err != nil {
		t.Fatal(err)
	}
	return p, group
}

// TestStartDoesNotBlock proves the block-commit loop is never stalled: Start
// must return in microseconds regardless of how long the VDF evaluation
// itself takes.
func TestStartDoesNotBlock(t *testing.T) {
	p, _ := testPipeline(t)
	s := NewVDFSealer(p)
	mix := randao.Seed{0xAB, 0xCD}

	begin := time.Now()
	s.Start(testEpoch, mix)
	elapsed := time.Since(begin)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("Start blocked for %v - it must launch the goroutine and return immediately", elapsed)
	}
}

// TestResultFailsClosedBeforeReady is the core safety property: a caller that
// checks before the goroutine finishes must get an explicit "not ready"
// signal, never a zero-value proof mistaken for a real one.
func TestResultFailsClosedBeforeReady(t *testing.T) {
	p, _ := testPipeline(t)
	s := NewVDFSealer(p)
	s.Start(testEpoch, randao.Seed{0xAB, 0xCD})

	if _, ok := s.Result(); ok {
		t.Fatal("Result reported ready before the goroutine could possibly have finished")
	}
}

// TestResultDeliversAfterCompletion confirms the happy path: once the
// goroutine finishes, Result reports ready with the real proof, bound to the
// epoch it was sealed for.
func TestResultDeliversAfterCompletion(t *testing.T) {
	p, group := testPipeline(t)
	s := NewVDFSealer(p)
	mix := randao.Seed{0xAB, 0xCD}
	s.Start(testEpoch, mix)

	var (
		got SealResult
		ok  bool
	)
	deadline := time.After(5 * time.Second)
	for !ok {
		select {
		case <-deadline:
			t.Fatal("proof never became ready")
		default:
			got, ok = s.Result()
		}
	}

	if got.Err != nil {
		t.Fatalf("sealing failed: %v", got.Err)
	}
	if got.ForEpoch != testEpoch {
		t.Fatalf("ForEpoch = %d, want %d", got.ForEpoch, testEpoch)
	}
	if !vdf.Verify(group, mix[:], got.Proof) {
		t.Fatal("delivered proof does not verify against the mix it was sealed for")
	}
}

// TestResultIsSingleShot documents that draining the channel once means a
// second call reports not-ready, even though the goroutine is long done -
// callers must keep the value from their first successful Result call.
func TestResultIsSingleShot(t *testing.T) {
	p, _ := testPipeline(t)
	s := NewVDFSealer(p)
	s.Start(testEpoch, randao.Seed{0xAB, 0xCD})

	deadline := time.After(5 * time.Second)
	for {
		if _, ok := s.Result(); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("proof never became ready")
		default:
		}
	}

	if _, ok := s.Result(); ok {
		t.Fatal("a second Result call reported ready again - the channel should be drained")
	}
}

// TestSealingIsDeterministic is the property the whole "all nodes compute
// independently" design rests on (Low-Level-Design §1): two sealers, same
// group/mix/difficulty, must produce byte-identical proofs, so a node that
// misses the network proof and reseals itself always agrees with everyone
// else.
func TestSealingIsDeterministic(t *testing.T) {
	group := testGroup(t)
	mix := randao.Seed{0xAB, 0xCD}

	sinkA, _ := committee.NewBeaconSource(committee.MinRetainedEpochs)
	a, err := beacon.New(group, testDifficulty, sinkA)
	if err != nil {
		t.Fatal(err)
	}
	sinkB, _ := committee.NewBeaconSource(committee.MinRetainedEpochs)
	b, err := beacon.New(group, testDifficulty, sinkB)
	if err != nil {
		t.Fatal(err)
	}

	sealerA := NewVDFSealer(a)
	sealerB := NewVDFSealer(b)
	sealerA.Start(testEpoch, mix)
	sealerB.Start(testEpoch, mix)

	resultA := waitForResult(t, sealerA)
	resultB := waitForResult(t, sealerB)

	if resultA.Err != nil || resultB.Err != nil {
		t.Fatalf("sealing failed: a=%v b=%v", resultA.Err, resultB.Err)
	}
	if resultA.Proof.Y.Cmp(resultB.Proof.Y) != 0 || resultA.Proof.Pi.Cmp(resultB.Proof.Pi) != 0 {
		t.Fatal("two independent sealers of the same input produced different proofs")
	}
}

func waitForResult(t *testing.T, s *VDFSealer) SealResult {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if r, ok := s.Result(); ok {
			return r
		}
		select {
		case <-deadline:
			t.Fatal("proof never became ready")
		default:
		}
	}
}
