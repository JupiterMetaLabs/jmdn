package DB_OPs

import (
	"os"
	"strconv"
	"strings"
)

// accountsPoolMaxEnv overrides the accounts-DB pool's MaxConnections.
//
// The pool cap is otherwise the hard-coded config.DefaultConnectionPoolConfig
// value (30), shared by every pool. This knob lets an operator raise the
// accounts pool alone on a node that serves heavy read traffic (the sequencer)
// without a code change. Unset or invalid → the default is kept.
//
// ImmuDB's --max-sessions limit does not apply here: pooled connections
// authenticate with the legacy Login token API, which does not create a server
// session (immudb v1.10.0 pkg/server/user.go Login → auth.GenerateToken).
const accountsPoolMaxEnv = "JMDN_ACCOUNTS_POOL_MAX"

// accountsPoolMaxCeiling bounds the override so a typo cannot open hundreds of
// gRPC/TLS connections to ImmuDB.
const accountsPoolMaxCeiling = 256

// accountsPoolMax returns the effective accounts pool cap.
// Values below def are ignored: the override may only raise the cap.
// Time: O(1).
func accountsPoolMax(def int) int {
	return envIntInRange(accountsPoolMaxEnv, def, def, accountsPoolMaxCeiling)
}

// envIntInRange parses key as a base-10 int and returns it when it lies in
// [lo, hi]; otherwise it returns def. Never panics.
func envIntInRange(key string, def, lo, hi int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		return def
	}
	return v
}
