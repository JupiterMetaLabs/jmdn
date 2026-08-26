package txstatus

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSubmitLog_RecordAndGet(t *testing.T) {
	l := NewSubmitLog(time.Minute, 10)
	l.Record(SubmitRecord{Hash: testHash, Sender: "0xaa", Nonce: 3, Forwarded: true})

	got, ok := l.Get(testHash)
	if !ok {
		t.Fatal("record not found")
	}
	if got.Sender != "0xaa" || got.Nonce != 3 || !got.Forwarded {
		t.Errorf("record round-tripped incorrectly: %+v", got)
	}
	if got.SubmittedAt.IsZero() {
		t.Error("SubmittedAt should be stamped when not supplied")
	}
}

// The log is jmdn's own map, so keying it consistently is safe and stops one
// transaction becoming two records because a caller changed the casing.
func TestSubmitLog_NormalisesKeys(t *testing.T) {
	l := NewSubmitLog(time.Minute, 10)
	l.Record(SubmitRecord{Hash: "0xABCDEF", Forwarded: true})

	for _, probe := range []string{"0xABCDEF", "0xabcdef", "abcdef", "  0xAbCdEf  "} {
		if _, ok := l.Get(probe); !ok {
			t.Errorf("Get(%q) missed a record stored as 0xABCDEF", probe)
		}
	}
	if l.Len() != 1 {
		t.Errorf("Len = %d, want 1 — casing produced duplicate records", l.Len())
	}
}

func TestSubmitLog_ExpiresRecords(t *testing.T) {
	l := NewSubmitLog(100*time.Millisecond, 10)
	now := time.Now()
	l.now = func() time.Time { return now }

	l.Record(SubmitRecord{Hash: testHash, Forwarded: true})
	if _, ok := l.Get(testHash); !ok {
		t.Fatal("record should be live immediately after recording")
	}

	now = now.Add(150 * time.Millisecond)
	if _, ok := l.Get(testHash); ok {
		t.Error("record outlived its TTL")
	}
	if l.Len() != 0 {
		t.Error("an expired record should be dropped when touched")
	}
}

func TestSubmitLog_RespectsCapacity(t *testing.T) {
	const cap = 20
	l := NewSubmitLog(time.Hour, cap)

	for i := 0; i < cap*5; i++ {
		l.Record(SubmitRecord{Hash: fmt.Sprintf("0x%04x", i), Forwarded: true})
		if l.Len() > cap {
			t.Fatalf("after %d records Len = %d, exceeding capacity %d", i+1, l.Len(), cap)
		}
	}
}

// Eviction under capacity pressure must drop the OLDEST records: those are the
// ones most likely already mined, and losing a young record would wrongly
// downgrade a live in-flight transaction to `unknown`.
func TestSubmitLog_EvictsOldestFirst(t *testing.T) {
	l := NewSubmitLog(time.Hour, 10)
	base := time.Now()
	l.now = func() time.Time { return base }

	for i := 0; i < 10; i++ {
		l.Record(SubmitRecord{
			Hash:        fmt.Sprintf("0x%04x", i),
			SubmittedAt: base.Add(time.Duration(i) * time.Minute),
			Forwarded:   true,
		})
	}

	// One more forces eviction.
	l.Record(SubmitRecord{Hash: "0xnew", SubmittedAt: base.Add(time.Hour), Forwarded: true})

	if _, ok := l.Get("0xnew"); !ok {
		t.Error("the newest record was not retained")
	}
	if _, ok := l.Get("0x0000"); ok {
		t.Error("the oldest record survived eviction while newer ones were present")
	}
}

// Replacing an existing key at capacity must not trigger eviction — a resubmit
// should not cost an unrelated in-flight record.
func TestSubmitLog_ReplaceAtCapacityDoesNotEvict(t *testing.T) {
	l := NewSubmitLog(time.Hour, 4)
	for i := 0; i < 4; i++ {
		l.Record(SubmitRecord{Hash: fmt.Sprintf("0x%04x", i), Forwarded: true})
	}
	before := l.Len()

	l.Record(SubmitRecord{Hash: "0x0002", Forwarded: false, ForwardErr: "retry failed"})

	if l.Len() != before {
		t.Errorf("Len = %d after replacing an existing key, want %d", l.Len(), before)
	}
	got, ok := l.Get("0x0002")
	if !ok {
		t.Fatal("replaced record missing")
	}
	if got.Forwarded {
		t.Error("replacement did not overwrite the previous record")
	}
}

// A zero TTL or capacity disables the log. That makes `processing` unreachable
// and every in-flight transaction report `unknown` — the safe direction, and it
// must not panic or misbehave.
func TestSubmitLog_DisabledWhenUnsized(t *testing.T) {
	for name, l := range map[string]*SubmitLog{
		"zero ttl":      NewSubmitLog(0, 10),
		"zero capacity": NewSubmitLog(time.Minute, 0),
	} {
		t.Run(name, func(t *testing.T) {
			l.Record(SubmitRecord{Hash: testHash, Forwarded: true})
			if _, ok := l.Get(testHash); ok {
				t.Error("a disabled log returned a record")
			}
			if l.Len() != 0 {
				t.Errorf("Len = %d on a disabled log", l.Len())
			}
		})
	}
}

// The submit path calls Record unconditionally, so a nil log must be a silent
// no-op rather than a panic on the transaction hot path.
func TestSubmitLog_NilReceiverIsSafe(t *testing.T) {
	var l *SubmitLog

	l.Record(SubmitRecord{Hash: testHash, Forwarded: true}) // must not panic
	if _, ok := l.Get(testHash); ok {
		t.Error("nil log returned a record")
	}
	if l.Len() != 0 {
		t.Error("nil log reported a non-zero length")
	}
}

func TestSubmitLog_EmptyHashIgnored(t *testing.T) {
	l := NewSubmitLog(time.Minute, 10)
	l.Record(SubmitRecord{Hash: "   ", Forwarded: true})
	if l.Len() != 0 {
		t.Error("an empty hash was recorded")
	}
	if _, ok := l.Get(""); ok {
		t.Error("Get(\"\") returned a record")
	}
}

func TestSubmitLog_ConcurrentAccessIsRaceFree(t *testing.T) {
	l := NewSubmitLog(time.Minute, 256)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				h := fmt.Sprintf("0x%02x%02x", i, j%50)
				l.Record(SubmitRecord{Hash: h, Forwarded: true})
				_, _ = l.Get(h)
				_ = l.Len()
			}
		}(i)
	}
	wg.Wait()
}

// The process-wide accessor must be usable before InitSubmitLog runs — the
// submit path may fire before (or entirely without) the feature being enabled.
func TestGlobalSubmitLog_UsableBeforeInit(t *testing.T) {
	RecordSubmit(SubmitRecord{Hash: "0xdeadbeef", Forwarded: true}) // must not panic

	l := InitSubmitLog(time.Minute, 10)
	if l == nil {
		t.Fatal("InitSubmitLog returned nil")
	}
	RecordSubmit(SubmitRecord{Hash: "0xfeedface", Forwarded: true})
	if _, ok := GlobalSubmitLog().Get("0xfeedface"); !ok {
		t.Error("RecordSubmit did not reach the installed global log")
	}
}

func TestNormalizeHash(t *testing.T) {
	cases := map[string]string{
		"0xABC":    "0xabc",
		"abc":      "0xabc",
		"  0xAbC ": "0xabc",
		"":         "",
		"   ":      "",
	}
	for in, want := range cases {
		if got := normalizeHash(in); got != want {
			t.Errorf("normalizeHash(%q) = %q, want %q", in, got, want)
		}
	}
}
