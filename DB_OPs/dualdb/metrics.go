package dualdb

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const ringSize = 1024

type OpMetrics struct {
	TotalWrites atomic.Int64
	ThebeErrors atomic.Int64

	mu     sync.Mutex
	deltas [ringSize]int64
	pos    int
}

func (m *OpMetrics) RecordDelta(d time.Duration) {
	m.mu.Lock()
	m.deltas[m.pos%ringSize] = d.Nanoseconds()
	m.pos++
	m.mu.Unlock()
}

func (m *OpMetrics) snapshot() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := m.pos
	if count > ringSize {
		count = ringSize
	}

	out := make([]int64, count)
	copy(out, m.deltas[:count])
	return out
}

func (m *OpMetrics) AvgLatencyDeltaMs() float64 {
	snap := m.snapshot()
	if len(snap) == 0 {
		return 0
	}

	var sum int64
	for _, v := range snap {
		sum += v
	}

	return float64(sum) / float64(len(snap)) / 1e6
}

func (m *OpMetrics) P99LatencyDeltaMs() float64 {
	snap := m.snapshot()
	if len(snap) == 0 {
		return 0
	}

	sort.Slice(snap, func(i, j int) bool { return snap[i] < snap[j] })
	idx := int(float64(len(snap)) * 0.99)
	if idx >= len(snap) {
		idx = len(snap) - 1
	}

	return float64(snap[idx]) / 1e6
}

func (m *OpMetrics) ThebeErrorRate() float64 {
	total := m.TotalWrites.Load()
	if total == 0 {
		return 0
	}

	return float64(m.ThebeErrors.Load()) / float64(total)
}

type Report struct {
	TotalWrites       int64   `json:"total_writes"`
	ThebeErrorRate    float64 `json:"thebe_error_rate"`
	AvgLatencyDeltaMs float64 `json:"avg_latency_delta_ms"`
	P99LatencyDeltaMs float64 `json:"p99_latency_delta_ms"`
}
