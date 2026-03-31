package repository

import (
	"context"
	"fmt"
	"sync"
	"time"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
)

// RunStatus represents the lifecycle state of a backfill run.
type RunStatus string

const (
	StatusIdle    RunStatus = "idle"
	StatusRunning RunStatus = "running"
	StatusDone    RunStatus = "done"
	StatusFailed  RunStatus = "failed"
	StatusStopped RunStatus = "stopped"
)

// Progress holds a live snapshot of the active (or last completed) backfill run.
// Fields are designed so a future persistence layer can write them directly to a
// backfill_runs table without structural changes.
type Progress struct {
	Status       RunStatus `json:"status"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
	CurrentBlock uint64    `json:"current_block"`
	BlocksDone   uint64    `json:"blocks_done"`
	ErrorCount   int       `json:"error_count"`
	LastError    string    `json:"last_error,omitempty"`
}

// BackfillManager controls the lifecycle of a backfill run.
// Only one run may be active at a time; duplicate Start calls are rejected.
type BackfillManager struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	progress Progress

	source CoordinatorRepository
	target CoordinatorRepository
	thebe  *thebedb.ThebeDB
	cfg    Config
}

// NewBackfillManager creates a manager. cfg.Enabled is ignored here — Start() is the explicit trigger.
func NewBackfillManager(
	source CoordinatorRepository,
	target CoordinatorRepository,
	thebe *thebedb.ThebeDB,
	cfg Config,
) *BackfillManager {
	return &BackfillManager{
		source:   source,
		target:   target,
		thebe:    thebe,
		cfg:      cfg,
		progress: Progress{Status: StatusIdle},
	}
}

// Start kicks off a backfill run in the background.
// Returns an error if a run is already active.
func (m *BackfillManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.progress.Status == StatusRunning {
		return fmt.Errorf("backfill already running (started at %s)", m.progress.StartedAt.Format(time.RFC3339))
	}

	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.progress = Progress{
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}

	cfg := m.cfg
	cfg.Enabled = true

	worker := NewBackfillWorker(m.source, m.target, m.thebe, cfg)
	worker.onProgress = func(current uint64, errCount int) {
		m.mu.Lock()
		m.progress.CurrentBlock = current
		m.progress.BlocksDone++
		m.progress.ErrorCount = errCount
		m.mu.Unlock()
	}
	worker.onError = func(msg string) {
		m.mu.Lock()
		m.progress.LastError = msg
		m.mu.Unlock()
	}

	go func() {
		err := worker.Run(runCtx)

		m.mu.Lock()
		defer m.mu.Unlock()
		m.cancel = nil
		m.progress.FinishedAt = time.Now()

		switch {
		case runCtx.Err() != nil:
			m.progress.Status = StatusStopped
		case err != nil:
			m.progress.Status = StatusFailed
			m.progress.LastError = err.Error()
		default:
			m.progress.Status = StatusDone
		}
	}()

	return nil
}

// Stop cancels the active run. No-op if idle.
func (m *BackfillManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// Status returns an immutable snapshot of the current progress.
func (m *BackfillManager) Status() Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress
}
