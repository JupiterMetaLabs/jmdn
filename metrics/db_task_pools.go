package metrics

import (
	"database/sql"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// DBTaskPoolSample is one task pool's live state at scrape time.
type DBTaskPoolSample struct {
	Task    string
	MaxOpen int
	Stats   sql.DBStats
}

var (
	dbTaskPoolDesc = struct {
		maxOpen, open, inUse, idle, waitCount, waitSeconds *prometheus.Desc
	}{
		maxOpen:     prometheus.NewDesc("jmdn_db_task_pool_max_open", "Configured connection budget of the task pool.", []string{"task"}, nil),
		open:        prometheus.NewDesc("jmdn_db_task_pool_open", "Open connections (in use + idle).", []string{"task"}, nil),
		inUse:       prometheus.NewDesc("jmdn_db_task_pool_in_use", "Connections currently in use. Alert when close to max_open.", []string{"task"}, nil),
		idle:        prometheus.NewDesc("jmdn_db_task_pool_idle", "Idle connections.", []string{"task"}, nil),
		waitCount:   prometheus.NewDesc("jmdn_db_task_pool_wait_total", "Total waits for a connection because the budget was exhausted.", []string{"task"}, nil),
		waitSeconds: prometheus.NewDesc("jmdn_db_task_pool_wait_seconds_total", "Total time spent waiting for a connection.", []string{"task"}, nil),
	}
	dbTaskPoolOnce sync.Once
)

type dbTaskPoolCollector struct{ sample func() []DBTaskPoolSample }

func (c dbTaskPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	d := dbTaskPoolDesc
	for _, x := range []*prometheus.Desc{d.maxOpen, d.open, d.inUse, d.idle, d.waitCount, d.waitSeconds} {
		ch <- x
	}
}

func (c dbTaskPoolCollector) Collect(ch chan<- prometheus.Metric) {
	d := dbTaskPoolDesc
	for _, s := range c.sample() {
		ch <- prometheus.MustNewConstMetric(d.maxOpen, prometheus.GaugeValue, float64(s.MaxOpen), s.Task)
		ch <- prometheus.MustNewConstMetric(d.open, prometheus.GaugeValue, float64(s.Stats.OpenConnections), s.Task)
		ch <- prometheus.MustNewConstMetric(d.inUse, prometheus.GaugeValue, float64(s.Stats.InUse), s.Task)
		ch <- prometheus.MustNewConstMetric(d.idle, prometheus.GaugeValue, float64(s.Stats.Idle), s.Task)
		ch <- prometheus.MustNewConstMetric(d.waitCount, prometheus.CounterValue, float64(s.Stats.WaitCount), s.Task)
		ch <- prometheus.MustNewConstMetric(d.waitSeconds, prometheus.CounterValue, s.Stats.WaitDuration.Seconds(), s.Task)
	}
}

// RegisterDBTaskPools exposes the task-wise DB pools (DB_OPs/task_pools.go) on
// DefaultRegistry. sample is called at scrape time. Registers once; later calls
// are no-ops.
func RegisterDBTaskPools(sample func() []DBTaskPoolSample) {
	if sample == nil {
		return
	}
	dbTaskPoolOnce.Do(func() {
		DefaultRegistry.MustRegister(dbTaskPoolCollector{sample: sample})
	})
}
