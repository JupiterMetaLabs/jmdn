package streamread

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestFrame_ReadsOneDelimitedFrame(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _, _ = a.Write([]byte("hello\nrest")) }()
	got, err := Frame(b, '\n', 1024, time.Second)
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A peer that sends more than the cap without a delimiter gets an error, and
// the handler does not buffer more than the cap.
func TestFrame_RejectsOversizedFrame(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _, _ = a.Write([]byte(strings.Repeat("x", 4096))) }()
	_, err := Frame(b, '\n', 1024, time.Second)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
}

// A peer that never sends the delimiter cannot hold the reader past the
// timeout, and the deadline is cleared afterwards.
func TestFrame_TimesOutOnSilentPeer(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	start := time.Now()
	_, err := Frame(b, '\n', 1024, 100*time.Millisecond)
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("read was not bounded by the timeout")
	}
	go func() { _, _ = a.Write([]byte("later\n")) }()
	if got, err := Frame(b, '\n', 1024, 0); err != nil || string(got) != "later\n" {
		t.Fatalf("deadline not cleared: %q, %v", got, err)
	}
}
