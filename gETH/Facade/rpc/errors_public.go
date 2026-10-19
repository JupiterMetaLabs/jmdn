package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/JupiterMetaLabs/ion"
)

// internalErrorMarkers identify error text that describes this node's
// infrastructure (database drivers, storage engine, network dials, file
// paths, runtime state) rather than the caller's request. Such text is
// logged, and the client gets a generic message with a reference to it.
var internalErrorMarkers = []string{
	"sql:", "pq:", "pgx", "postgres", "badger", "sqlite", "duckdb",
	"dial tcp", "connection refused", "connection reset", "broken pipe",
	"i/o timeout", "no such file", "permission denied", "/opt/", "/home/",
	"/var/", "/etc/", "goroutine ", "panic", "runtime error", "nil pointer",
	"context deadline exceeded", "too many open files",
}

// publicErrorMessage returns the text to send a client for err. Request
// errors (validation, reverts, unknown hashes) pass through unchanged;
// anything that looks like an internal failure is replaced by a generic
// message carrying a reference that appears in this node's log next to the
// full error.
func publicErrorMessage(ctx context.Context, err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)
	// A missing row is the caller asking for something that does not exist,
	// not a node failure; say so without the driver text.
	if strings.Contains(lower, "no rows in result set") {
		return "not found"
	}
	for _, m := range internalErrorMarkers {
		if strings.Contains(lower, m) {
			return internalErrorMessage(ctx, err)
		}
	}
	return msg
}

// internalErrorMessage always hides err; for handlers whose failures are
// internal by construction (HTTP 500 paths).
func internalErrorMessage(ctx context.Context, err error) string {
	ref := errorRef()
	if l := logger(); l != nil {
		l.Error(ctx, "RPC internal error", err, ion.String("ref", ref))
	}
	return "internal error (ref " + ref + ")"
}

func errorRef() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
