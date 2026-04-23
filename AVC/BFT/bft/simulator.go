// =============================================================================
// FILE: pkg/bft/simulator.go (for testing)
// =============================================================================
package bft

import (
	"math/rand"
	"sync"
	"time"
)

// Simulator simulates network for testing
type Simulator struct {
	mu sync.RWMutex

	prepareChans map[string]chan *PrepareMessage
	commitChans  map[string]chan *CommitMessage

	delay     time.Duration
	dropRate  float64
	byzantine map[string]bool
}

// NewSimulator creates a network simulator
func NewSimulator(buddyIDs []string) *Simulator {
	prepareChans := make(map[string]chan *PrepareMessage)
	commitChans := make(map[string]chan *CommitMessage)

	for _, id := range buddyIDs {
		prepareChans[id] = make(chan *PrepareMessage, 200)
		commitChans[id] = make(chan *CommitMessage, 200)
	}

	return &Simulator{
		prepareChans: prepareChans,
		commitChans:  commitChans,
		byzantine:    make(map[string]bool),
	}
}

// ForBuddy creates a messenger for specific buddy
func (s *Simulator) ForBuddy(buddyID string) *SimulatorMessenger {
	return &SimulatorMessenger{
		sim:     s,
		buddyID: buddyID,
	}
}

// SetDelay sets network delay
func (s *Simulator) SetDelay(delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = delay
}

// SetDropRate sets message drop rate (0.0-1.0)
func (s *Simulator) SetDropRate(rate float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropRate = rate
}

// MarkByzantine marks buddy as Byzantine
func (s *Simulator) MarkByzantine(buddyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byzantine[buddyID] = true
}

func (s *Simulator) broadcastPrepare(from string, msg *PrepareMessage) error {
	s.mu.RLock()
	delay := s.delay
	dropRate := s.dropRate
	isByzantine := s.byzantine[from]
	s.mu.RUnlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	// Byzantine behavior: send conflicting messages
	if isByzantine {
		half := len(s.prepareChans) / 2
		count := 0

		for buddyID, ch := range s.prepareChans {
			if buddyID == from {
				continue
			}

			conflictMsg := *msg
			if count < half {
				conflictMsg.Decision = Accept
			} else {
				conflictMsg.Decision = Reject
			}
			count++

			if !s.shouldDrop(dropRate) {
				select {
				case ch <- &conflictMsg:
				default:
				}
			}
		}
		return nil
	}

	// Normal broadcast
	for buddyID, ch := range s.prepareChans {
		if buddyID == from {
			continue
		}

		if !s.shouldDrop(dropRate) {
			select {
			case ch <- msg:
			default:
			}
		}
	}

	return nil
}

func (s *Simulator) broadcastCommit(from string, msg *CommitMessage) error {
	s.mu.RLock()
	delay := s.delay
	dropRate := s.dropRate
	s.mu.RUnlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	for buddyID, ch := range s.commitChans {
		if buddyID == from {
			continue
		}

		if !s.shouldDrop(dropRate) {
			select {
			case ch <- msg:
			default:
			}
		}
	}

	return nil
}

func (s *Simulator) shouldDrop(dropRate float64) bool {
	if dropRate == 0 {
		return false
	}
	return rand.Float64() < dropRate
}

// SimulatorMessenger implements Messenger for testing
type SimulatorMessenger struct {
	sim     *Simulator
	buddyID string
}

func (m *SimulatorMessenger) BroadcastPrepare(msg *PrepareMessage) error {
	return m.sim.broadcastPrepare(m.buddyID, msg)
}

func (m *SimulatorMessenger) BroadcastCommit(msg *CommitMessage) error {
	return m.sim.broadcastCommit(m.buddyID, msg)
}

func (m *SimulatorMessenger) ReceivePrepare() <-chan *PrepareMessage {
	m.sim.mu.RLock()
	defer m.sim.mu.RUnlock()
	return m.sim.prepareChans[m.buddyID]
}

func (m *SimulatorMessenger) ReceiveCommit() <-chan *CommitMessage {
	m.sim.mu.RLock()
	defer m.sim.mu.RUnlock()
	return m.sim.commitChans[m.buddyID]
}
