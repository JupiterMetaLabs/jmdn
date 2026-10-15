package DB_OPs

// accountsPoolMaxCeiling bounds database.accounts_pool_max so a typo cannot
// open hundreds of gRPC/TLS connections to ImmuDB.
//
// ImmuDB's --max-sessions limit does not apply to the pool: pooled connections
// authenticate with the legacy Login token API, which does not create a server
// session (immudb v1.10.0 pkg/server/user.go Login → auth.GenerateToken).
const accountsPoolMaxCeiling = 256

// accountsPoolMax returns the effective accounts pool cap for the configured
// database.accounts_pool_max. The setting may only raise the cap: a value
// below def (the shared pool default) or above accountsPoolMaxCeiling is
// rejected, in which case def is returned and ok is false.
// Time: O(1).
func accountsPoolMax(def, configured int) (maxConns int, ok bool) {
	if configured < def || configured > accountsPoolMaxCeiling {
		return def, false
	}
	return configured, true
}
