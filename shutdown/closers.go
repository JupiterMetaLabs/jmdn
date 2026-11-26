package shutdown

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Closers release the node's long-lived resources (ThebeDB / Badger, Postgres
// pools, the outbox worker, the libp2p host) at shutdown.
//
// Why this exists: main.go used to release them with `defer`. main() never
// returns while the node runs (it parks in select {}), and the process ends
// through os.Exit or a supervisor SIGKILL, neither of which runs deferred
// calls. So none of them was ever closed: Badger ran crash recovery on every
// start, and Postgres / P2P connections were cut instead of closed.
//
// Closers run in REVERSE registration order (last registered, first closed),
// like defer. Resources are created in dependency order, so this closes a user
// before what it uses (outbox worker before ThebeDB, ThebeDB before the pools).
//
// Each closer runs at most once, however many times RunClosers is called, so
// main can both register a closer and `defer RunClosers()` for the normal
// return path without double-closing.

// DefaultCloserTimeout bounds one closer when Register is given timeout <= 0.
const DefaultCloserTimeout = 3 * time.Second

// closersBudget bounds RunClosers as a whole. Shutdown spends ~0.4s on flush
// sleeps and up to gracefulShutdownTimeout (10s) on GRO first, and main.go's
// watchdog caps the whole sequence at 25s: 0.4s + 10s + 13s leaves ~1.6s for
// metrics printing and the logger. main.go runs the profiler shutdown
// concurrently, so it does not eat into this.
//
// The budget is also capped by SetDeadline: RunClosers never runs past the
// deadline main sets when the signal arrives, so the watchdog's os.Exit cannot
// land in the middle of a closer (ThebeDB's Badger Close in particular). A
// closer with no time left is skipped and logged instead.
const closersBudget = 13 * time.Second

var (
	deadlineMu sync.Mutex
	deadline   time.Time
)

// SetDeadline sets the time by which RunClosers must have finished. main sets
// it when the shutdown signal arrives, a margin before its watchdog fires. The
// zero time means no deadline beyond closersBudget.
func SetDeadline(t time.Time) {
	deadlineMu.Lock()
	deadline = t
	deadlineMu.Unlock()
}

// effectiveBudget is budget, shortened to the time left before the deadline
// set with SetDeadline.
func effectiveBudget(budget time.Duration) time.Duration {
	deadlineMu.Lock()
	d := deadline
	deadlineMu.Unlock()
	if d.IsZero() {
		return budget
	}
	if left := time.Until(d); left < budget {
		return left
	}
	return budget
}

type closer struct {
	name    string
	timeout time.Duration
	fn      func() error
	once    sync.Once
}

var (
	closersMu sync.Mutex
	closers   []*closer
)

// Register adds a resource to release at shutdown. timeout bounds this one
// closer (DefaultCloserTimeout if <= 0); a closer that does not return in time
// is abandoned so one stuck resource cannot block exit. A nil fn is ignored.
func Register(name string, timeout time.Duration, fn func() error) {
	if fn == nil {
		return
	}
	if timeout <= 0 {
		timeout = DefaultCloserTimeout
	}
	closersMu.Lock()
	closers = append(closers, &closer{name: name, timeout: timeout, fn: fn})
	closersMu.Unlock()
}

// RegisterFunc is Register for a close function that returns no error.
func RegisterFunc(name string, timeout time.Duration, fn func()) {
	if fn == nil {
		return
	}
	Register(name, timeout, func() error { fn(); return nil })
}

// RunClosers releases every registered resource, newest first, and reports
// whether all of them closed cleanly in time. Safe to call more than once:
// each closer runs at most once.
func RunClosers() bool {
	return runClosers(closersBudget)
}

func runClosers(budget time.Duration) bool {
	closersMu.Lock()
	list := make([]*closer, len(closers))
	copy(list, closers)
	closersMu.Unlock()

	end := time.Now().Add(effectiveBudget(budget))
	ok := true
	for i := len(list) - 1; i >= 0; i-- {
		c := list[i]
		c.once.Do(func() {
			wait := c.timeout
			if left := time.Until(end); left < wait {
				wait = left
			}
			if wait <= 0 {
				_, _ = fmt.Fprintf(os.Stderr, "shutdown: closer %q skipped: shutdown budget used up\n", c.name)
				ok = false
				return
			}
			if !runOne(c, wait) {
				ok = false
			}
		})
	}
	return ok
}

// runOne runs a single closer with a time limit. On timeout the closer's
// goroutine is left running; the process is about to exit anyway.
func runOne(c *closer, wait time.Duration) bool {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- c.fn()
	}()
	select {
	case err := <-done:
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "shutdown: closer %q failed: %v\n", c.name, err)
			return false
		}
		return true
	case <-time.After(wait):
		_, _ = fmt.Fprintf(os.Stderr, "shutdown: closer %q did not finish within %s; abandoning it\n", c.name, wait)
		return false
	}
}
