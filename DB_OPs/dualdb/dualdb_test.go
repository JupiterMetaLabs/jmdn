package dualdb_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"gossipnode/DB_OPs/dualdb"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"go.uber.org/zap"
)

var errFake = errors.New("fake error")

type mockPrimary struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (m *mockPrimary) Create(_ *config.PooledConnection, _ string, _ interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.err
}

func (m *mockPrimary) SafeCreate(_ *config.ImmuClient, _ string, _ interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.err
}

func (m *mockPrimary) BatchCreate(_ *config.PooledConnection, _ map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.err
}

func (m *mockPrimary) CreateAccount(_ *config.PooledConnection, _ string, _ common.Address, _ map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.err
}

func (m *mockPrimary) UpdateAccountBalance(_ *config.PooledConnection, _ common.Address, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.err
}

func (m *mockPrimary) StoreZKBlock(_ *config.PooledConnection, _ *config.ZKBlock) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.err
}

type mockShadow struct {
	mu    sync.Mutex
	err   error
	calls int
	wait  chan struct{}
}

func (m *mockShadow) markCalled() {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.wait != nil {
		select {
		case <-m.wait:
		default:
			close(m.wait)
		}
	}
}

func (m *mockShadow) Create(_ *config.PooledConnection, _ string, _ interface{}) error {
	m.markCalled()
	return m.err
}

func (m *mockShadow) SafeCreate(_ *config.ImmuClient, _ string, _ interface{}) error {
	m.markCalled()
	return m.err
}

func (m *mockShadow) BatchCreate(_ *config.PooledConnection, _ map[string]interface{}) error {
	m.markCalled()
	return m.err
}

func (m *mockShadow) CreateAccount(_ *config.PooledConnection, _ string, _ common.Address, _ map[string]interface{}) error {
	m.markCalled()
	return m.err
}

func (m *mockShadow) UpdateAccountBalance(_ *config.PooledConnection, _ common.Address, _ string) error {
	m.markCalled()
	return m.err
}

func (m *mockShadow) StoreZKBlock(_ *config.PooledConnection, _ *config.ZKBlock) error {
	m.markCalled()
	return m.err
}

func newDual(p *mockPrimary, s *mockShadow, async bool) *dualdb.DualDB {
	return dualdb.New(p, s, dualdb.Config{Enabled: true, ShadowAsync: async}, zap.NewNop())
}

func TestDualDB_BothSuccess(t *testing.T) {
	p := &mockPrimary{}
	s := &mockShadow{}
	d := newDual(p, s, false)

	if err := d.Create(nil, "k", []byte("v")); err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	r := d.Report()
	if r.TotalWrites != 1 {
		t.Errorf("TotalWrites: want 1 got %d", r.TotalWrites)
	}
	if r.ThebeErrorRate != 0 {
		t.Errorf("ThebeErrorRate: want 0 got %f", r.ThebeErrorRate)
	}
	p.mu.Lock()
	pc := p.calls
	p.mu.Unlock()
	s.mu.Lock()
	sc := s.calls
	s.mu.Unlock()
	if pc != 1 {
		t.Errorf("primary calls: want 1 got %d", pc)
	}
	if sc != 1 {
		t.Errorf("shadow calls: want 1 got %d", sc)
	}
}

func TestDualDB_PrimaryFail(t *testing.T) {
	p := &mockPrimary{err: errFake}
	s := &mockShadow{}
	d := newDual(p, s, false)

	err := d.Create(nil, "k", []byte("v"))
	if err == nil {
		t.Fatal("expected error")
	}

	s.mu.Lock()
	sc := s.calls
	s.mu.Unlock()
	if sc != 0 {
		t.Errorf("shadow must not be called when primary fails, got %d calls", sc)
	}
	r := d.Report()
	if r.TotalWrites != 1 {
		t.Errorf("TotalWrites: want 1 got %d", r.TotalWrites)
	}
}

func TestDualDB_ShadowFail(t *testing.T) {
	p := &mockPrimary{}
	s := &mockShadow{err: errFake}
	d := newDual(p, s, false)

	if err := d.Create(nil, "k", []byte("v")); err != nil {
		t.Fatalf("shadow failure must not propagate: %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	r := d.Report()
	if r.ThebeErrorRate == 0 {
		t.Error("ThebeErrorRate should be > 0 after shadow fail")
	}
}

func TestDualDB_ShadowAsync(t *testing.T) {
	called := make(chan struct{})
	p := &mockPrimary{}
	s := &mockShadow{wait: called}
	d := newDual(p, s, true)

	done := make(chan struct{})
	go func() {
		_ = d.Create(nil, "k", []byte("v"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("primary did not return quickly")
	}

	select {
	case <-called:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shadow was never called")
	}
}
