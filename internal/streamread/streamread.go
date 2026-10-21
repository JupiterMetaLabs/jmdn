// Package streamread reads one delimited frame from a libp2p stream with a
// size cap and a read deadline, so a peer cannot make a handler buffer an
// unbounded message or hold its goroutine open by never sending a delimiter.
package streamread

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"time"
)

// Defaults for inbound frames.
const (
	// DefaultMaxBytes caps a frame that may carry a full block (consensus and
	// buddy-node messages). Matches config.MaxBlockMessageBytes.
	DefaultMaxBytes = 8 * 1024 * 1024
	// SmallMaxBytes caps control frames (requests, reveals, peer lists).
	SmallMaxBytes = 1 * 1024 * 1024
	// DefaultTimeout bounds how long a peer may take to deliver one frame.
	DefaultTimeout = 30 * time.Second
)

// ErrFrameTooLarge is returned when no delimiter appears within the cap.
var ErrFrameTooLarge = errors.New("stream frame exceeds size limit")

// deadliner is the part of network.Stream this package needs.
type deadliner interface {
	io.Reader
	SetReadDeadline(time.Time) error
}

// Frame reads up to and including delim from s, reading at most maxBytes,
// within timeout. The read deadline is cleared afterwards so the handler can
// keep using the stream (e.g. to write a reply). The returned frame includes
// the delimiter. A stream that ends cleanly without a delimiter returns the
// bytes read and io.EOF, like bufio.Reader.ReadBytes.
func Frame(s deadliner, delim byte, maxBytes int64, timeout time.Duration) ([]byte, error) {
	if timeout > 0 {
		_ = s.SetReadDeadline(time.Now().Add(timeout))
		defer func() { _ = s.SetReadDeadline(time.Time{}) }()
	}
	r := bufio.NewReader(io.LimitReader(s, maxBytes+1))
	b, err := r.ReadBytes(delim)
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("%w (%d bytes)", ErrFrameTooLarge, maxBytes)
	}
	return b, err
}
