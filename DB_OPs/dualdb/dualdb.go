// Cutover signal: when Report().ThebeErrorRate == 0.000 for 72 continuous
// hours and Report().AvgLatencyDeltaMs < 5, ThebeDB is ready to be promoted
// to primary and immudb retired. Do not cut over before both conditions are met.
package dualdb

import (
	"context"
	"time"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"go.uber.org/zap"
)

// PrimaryWriter mirrors the exact immudb write signatures used in DB_OPs.
type PrimaryWriter interface {
	Create(pooledConnection *config.PooledConnection, key string, value interface{}) error
	SafeCreate(ic *config.ImmuClient, key string, value interface{}) error
	BatchCreate(pooledConnection *config.PooledConnection, entries map[string]interface{}) error
	CreateAccount(pooledConnection *config.PooledConnection, didAddress string, address common.Address, metadata map[string]interface{}) error
	UpdateAccountBalance(pooledConnection *config.PooledConnection, address common.Address, newBalance string) error
	StoreZKBlock(mainDBClient *config.PooledConnection, block *config.ZKBlock) error
}

// ShadowWriter mirrors the same write signatures and routes to cassata ingest.
type ShadowWriter interface {
	Create(pooledConnection *config.PooledConnection, key string, value interface{}) error
	SafeCreate(ic *config.ImmuClient, key string, value interface{}) error
	BatchCreate(pooledConnection *config.PooledConnection, entries map[string]interface{}) error
	CreateAccount(pooledConnection *config.PooledConnection, didAddress string, address common.Address, metadata map[string]interface{}) error
	UpdateAccountBalance(pooledConnection *config.PooledConnection, address common.Address, newBalance string) error
	StoreZKBlock(mainDBClient *config.PooledConnection, block *config.ZKBlock) error
}

type Config struct {
	Enabled     bool `yaml:"enabled"`      // default false
	ShadowAsync bool `yaml:"shadow_async"` // default true
}

// DualDB wraps immudb (primary) + cassata/ThebeDB (shadow).
// Primary failure -> hard fail. Shadow failure -> log + metric, succeed anyway.
type DualDB struct {
	primary PrimaryWriter
	shadow  ShadowWriter
	cfg     Config
	metrics *OpMetrics
	logger  *zap.Logger
}

func New(primary PrimaryWriter, shadow ShadowWriter, cfg Config, logger *zap.Logger) *DualDB {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &DualDB{
		primary: primary,
		shadow:  shadow,
		cfg:     cfg,
		metrics: &OpMetrics{},
		logger:  logger,
	}
}

func (d *DualDB) Report() Report {
	return Report{
		TotalWrites:       d.metrics.TotalWrites.Load(),
		ThebeErrorRate:    d.metrics.ThebeErrorRate(),
		AvgLatencyDeltaMs: d.metrics.AvgLatencyDeltaMs(),
		P99LatencyDeltaMs: d.metrics.P99LatencyDeltaMs(),
	}
}

func (d *DualDB) shadowWrite(_ context.Context, fn func() error) {
	do := func() {
		start := time.Now()
		if err := fn(); err != nil {
			d.metrics.ThebeErrors.Add(1)
			d.logger.Warn("dualdb: shadow write failed", zap.Error(err))
			return
		}
		d.metrics.RecordDelta(time.Since(start))
	}

	if d.cfg.ShadowAsync {
		go do()
	} else {
		do()
	}
}

func (d *DualDB) Create(pooledConnection *config.PooledConnection, key string, value interface{}) error {
	d.metrics.TotalWrites.Add(1)
	if err := d.primary.Create(pooledConnection, key, value); err != nil {
		return err
	}
	d.shadowWrite(context.Background(), func() error {
		return d.shadow.Create(pooledConnection, key, value)
	})
	return nil
}

func (d *DualDB) SafeCreate(ic *config.ImmuClient, key string, value interface{}) error {
	d.metrics.TotalWrites.Add(1)
	if err := d.primary.SafeCreate(ic, key, value); err != nil {
		return err
	}
	d.shadowWrite(context.Background(), func() error {
		return d.shadow.SafeCreate(ic, key, value)
	})
	return nil
}

func (d *DualDB) BatchCreate(pooledConnection *config.PooledConnection, entries map[string]interface{}) error {
	d.metrics.TotalWrites.Add(1)
	if err := d.primary.BatchCreate(pooledConnection, entries); err != nil {
		return err
	}
	d.shadowWrite(context.Background(), func() error {
		return d.shadow.BatchCreate(pooledConnection, entries)
	})
	return nil
}

func (d *DualDB) CreateAccount(pooledConnection *config.PooledConnection, didAddress string, address common.Address, metadata map[string]interface{}) error {
	d.metrics.TotalWrites.Add(1)
	if err := d.primary.CreateAccount(pooledConnection, didAddress, address, metadata); err != nil {
		return err
	}
	d.shadowWrite(context.Background(), func() error {
		return d.shadow.CreateAccount(pooledConnection, didAddress, address, metadata)
	})
	return nil
}

func (d *DualDB) UpdateAccountBalance(pooledConnection *config.PooledConnection, address common.Address, newBalance string) error {
	d.metrics.TotalWrites.Add(1)
	if err := d.primary.UpdateAccountBalance(pooledConnection, address, newBalance); err != nil {
		return err
	}
	d.shadowWrite(context.Background(), func() error {
		return d.shadow.UpdateAccountBalance(pooledConnection, address, newBalance)
	})
	return nil
}

func (d *DualDB) StoreZKBlock(mainDBClient *config.PooledConnection, block *config.ZKBlock) error {
	d.metrics.TotalWrites.Add(1)
	if err := d.primary.StoreZKBlock(mainDBClient, block); err != nil {
		return err
	}
	d.shadowWrite(context.Background(), func() error {
		return d.shadow.StoreZKBlock(mainDBClient, block)
	})
	return nil
}
