package config

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/JupiterMetaLabs/ion"
)

func newTestPool(t *testing.T, max int) *ConnectionPool {
	t.Helper()
	lg, _, err := ion.New(ion.Default())
	if err != nil {
		t.Fatalf("ion.New: %v", err)
	}
	return &ConnectionPool{
		Config: &ConnectionPoolConfig{MaxConnections: max, MaxLifetime: time.Hour,
			TokenMaxLifetime: 24 * time.Hour, TokenRefreshBuffer: time.Minute},
		Logger: lg,
	}
}

func idleConn() *PooledConnection {
	now := time.Now().UTC()
	return &PooledConnection{Client: &ImmuClient{}, CreatedAt: now, LastUsed: now, TokenExpiry: now.Add(24 * time.Hour)}
}

// Get must fail fast with the sentinel when every conn is in use, and the
// message must stay byte-identical for existing string matchers / log alerts.
func TestGet_ExhaustedReturnsSentinel(t *testing.T) {
	p := newTestPool(t, 2)
	p.Connections = []*PooledConnection{idleConn(), idleConn()}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := p.Get(ctx); err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
	}
	_, err := p.Get(ctx)
	if !errors.Is(err, ErrMaxConnectionsReached) {
		t.Fatalf("err = %v, want ErrMaxConnectionsReached", err)
	}
	if err.Error() != "maximum number of connections reached" {
		t.Fatalf("message changed: %q", err.Error())
	}
	wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", err))
	if !errors.Is(wrapped, ErrMaxConnectionsReached) {
		t.Fatal("sentinel lost through %w wrapping")
	}
}
