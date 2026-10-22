package database

// PoolStats contains statistics about a connection pool
type PoolStats struct {
	TotalConnections  int
	ActiveConnections int
	IdleConnections   int
}
