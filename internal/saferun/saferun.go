// Package saferun puts a panic boundary around network-ingress handlers.
//
// go-libp2p calls a stream handler on its own goroutine with no recover
// (basic_host.go), and Go aborts the whole process on an unrecovered panic in
// any goroutine, so a handler bug on one peer message or request would take
// the node down.
//
// These helpers turn such a panic into: a logged error with the stack, a
// jmdn_handler_panics_recovered_total{where} increment, and — for streams — a
// Reset so the remote sees the failure. They are a LAST line of defence; every
// handler must still validate its input. Handlers must not rely on them for
// control flow.
package saferun

import (
	"fmt"
	"runtime/debug"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/rs/zerolog/log"

	"gossipnode/metrics"
)

// report logs and counts a recovered panic.
func report(where string, r any) {
	metrics.HandlerPanicsCounter.WithLabelValues(where).Inc()
	log.Error().
		Str("where", where).
		Str("panic", fmt.Sprint(r)).
		Bytes("stack", debug.Stack()).
		Msg("panic recovered at network-ingress boundary — message dropped, node keeps running")
}

// RecoverInto is Recover plus a callback (e.g. to write an error response).
// onPanic runs only when a panic was recovered, after it has been reported.
func RecoverInto(where string, onPanic func(r any)) {
	if r := recover(); r != nil {
		report(where, r)
		if onPanic != nil {
			onPanic(r)
		}
	}
}

// Stream wraps a libp2p stream handler with a panic boundary. On panic the
// stream is Reset (the remote sees an error, not a hang) and the process keeps
// running. where should be the protocol ID.
func Stream(where string, h network.StreamHandler) network.StreamHandler {
	return func(s network.Stream) {
		defer func() {
			if r := recover(); r != nil {
				report(where, r)
				_ = s.Reset()
			}
		}()
		h(s)
	}
}
