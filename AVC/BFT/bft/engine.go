package bft

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type engine struct {
	config     Config
	myBuddyID  string
	myDecision Decision
	buddies    map[string]*BuddyInput
	threshold  int
	round      uint64
	blockHash  string

	byzantine *byzantineDetector

	mu          sync.RWMutex
	prepareMsgs map[string]*PrepareMessage
	commitMsgs  map[string]*CommitMessage

	seqMu       sync.RWMutex
	lastSeqSeen map[string]uint64
}

func (e *engine) runPrepare(ctx context.Context, messenger Messenger) (*PhaseResult, error) {
	myPrepare := &PrepareMessage{
		Version:   PrepareVersionV1,
		Seq:       e.ensureSeqForSelf(),
		Round:     e.round,
		BlockHash: e.blockHash,
		BuddyID:   e.myBuddyID,
		Decision:  e.myDecision,
		Timestamp: time.Now().Unix(),
	}

	e.addPrepareMsg(myPrepare)

	if err := messenger.BroadcastPrepare(myPrepare); err != nil {
		return nil, err
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, e.config.PrepareTimeout)
	defer cancel()

	go func() {
		for {
			select {
			case <-timeoutCtx.Done():
				return
			case msg := <-messenger.ReceivePrepare():
				if msg == nil {
					continue
				}
				if err := e.validatePrepare(msg); err == nil {
					e.addPrepareMsg(msg)
				}
			}
		}
	}()

	decision, err := e.waitForPrepareThreshold(timeoutCtx)
	if err != nil {
		return nil, err
	}

	acceptCount, rejectCount := e.getPrepareVoteCounts()
	return &PhaseResult{
		AcceptVotes:  acceptCount,
		RejectVotes:  rejectCount,
		ThresholdMet: true,
		Threshold:    e.threshold,
		Decision:     decision,
	}, nil
}

func (e *engine) runCommit(ctx context.Context, messenger Messenger, prepareDecision Decision) (*PhaseResult, error) {
	myCommit, err := e.createCommit(prepareDecision)
	if err != nil {
		return nil, err
	}

	e.addCommitMsg(myCommit)

	if err := messenger.BroadcastCommit(myCommit); err != nil {
		return nil, err
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, e.config.CommitTimeout)
	defer cancel()

	go func() {
		for {
			select {
			case <-timeoutCtx.Done():
				return
			case msg := <-messenger.ReceiveCommit():
				if msg == nil {
					continue
				}
				if err := e.validateCommit(msg); err != nil {
					continue
				}
				if byzantine := e.byzantine.detectConflicts(msg, e.prepareMsgs); len(byzantine) > 0 {
					for _, id := range byzantine {
						e.byzantine.mark(id)
					}
					continue
				}
				e.addCommitMsg(msg)
			}
		}
	}()

	decision, err := e.waitForCommitThreshold(timeoutCtx)
	if err != nil {
		return nil, err
	}

	acceptCount, rejectCount := e.getCommitVoteCountsClean()
	return &PhaseResult{
		AcceptVotes:  acceptCount,
		RejectVotes:  rejectCount,
		ThresholdMet: true,
		Threshold:    e.threshold,
		Decision:     decision,
	}, nil
}

func (e *engine) addPrepareMsg(msg *PrepareMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.prepareMsgs[msg.BuddyID]; !exists {
		e.prepareMsgs[msg.BuddyID] = msg
	}
}

func (e *engine) addCommitMsg(msg *CommitMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.commitMsgs[msg.BuddyID]; !exists {
		e.commitMsgs[msg.BuddyID] = msg
	}
}

func (e *engine) waitForPrepareThreshold(ctx context.Context) (Decision, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			acceptCount, rejectCount := e.getPrepareVoteCounts()
			return "", fmt.Errorf("PREPARE timeout: accepts=%d, rejects=%d, need=%d", acceptCount, rejectCount, e.threshold)
		case <-ticker.C:
			acceptCount, rejectCount := e.getPrepareVoteCounts()
			if acceptCount >= e.threshold {
				return Accept, nil
			}
			if rejectCount >= e.threshold {
				return Reject, nil
			}
		}
	}
}

func (e *engine) waitForCommitThreshold(ctx context.Context) (Decision, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			acceptCount, rejectCount := e.getCommitVoteCountsClean()
			return "", fmt.Errorf("COMMIT timeout: accepts=%d, rejects=%d, need=%d", acceptCount, rejectCount, e.threshold)
		case <-ticker.C:
			acceptCount, rejectCount := e.getCommitVoteCountsClean()
			if acceptCount >= e.threshold {
				return Accept, nil
			}
			if rejectCount >= e.threshold {
				return Reject, nil
			}
		}
	}
}

func (e *engine) getPrepareVoteCounts() (accept, reject int) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, msg := range e.prepareMsgs {
		if msg.Decision == Accept {
			accept++
		} else {
			reject++
		}
	}
	return
}

func (e *engine) getCommitVoteCountsClean() (accept, reject int) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for buddyID, msg := range e.commitMsgs {
		if e.byzantine.isByzantine(buddyID) {
			continue
		}
		if msg.Decision == Accept {
			accept++
		} else {
			reject++
		}
	}
	return
}

func (e *engine) collectPrepareProof(decision Decision) []PrepareMessage {
	e.mu.RLock()
	defer e.mu.RUnlock()

	proof := make([]PrepareMessage, 0, e.threshold)
	for _, msg := range e.prepareMsgs {
		if msg.Decision == decision {
			proof = append(proof, *msg)
			if len(proof) >= e.threshold {
				break
			}
		}
	}
	return proof
}

// SIMPLIFIED - No signature validation
func (e *engine) validatePrepare(msg *PrepareMessage) error {
	_, exists := e.buddies[msg.BuddyID]
	if !exists {
		return fmt.Errorf("unknown buddy")
	}

	if msg.Round != e.round || msg.BlockHash != e.blockHash {
		return fmt.Errorf("wrong round or block")
	}

	if !isTimestampFresh(msg.Timestamp) {
		return fmt.Errorf("stale timestamp")
	}

	if err := e.checkAndMarkSeq(msg.BuddyID, msg.Seq); err != nil {
		return fmt.Errorf("replay/seq error: %w", err)
	}

	return nil
}

// SIMPLIFIED - No signature validation
func (e *engine) validateCommit(msg *CommitMessage) error {
	_, exists := e.buddies[msg.BuddyID]
	if !exists {
		return fmt.Errorf("unknown buddy")
	}

	if msg.Round != e.round || msg.BlockHash != e.blockHash {
		return fmt.Errorf("wrong round or block")
	}

	if len(msg.PrepareProof) > e.config.MaxProofSize {
		return fmt.Errorf("prepare proof too large")
	}

	if !isTimestampFresh(msg.Timestamp) {
		return fmt.Errorf("stale timestamp")
	}

	if err := e.checkAndMarkSeq(msg.BuddyID, msg.Seq); err != nil {
		return fmt.Errorf("replay/seq error: %w", err)
	}

	// Validate proof has enough supporting votes
	supportingCount := 0
	seen := make(map[string]bool)

	for _, prepare := range msg.PrepareProof {
		if seen[prepare.BuddyID] {
			return fmt.Errorf("duplicate in proof")
		}
		seen[prepare.BuddyID] = true

		if _, ok := e.buddies[prepare.BuddyID]; !ok {
			return fmt.Errorf("unknown buddy in proof: %s", prepare.BuddyID)
		}

		if !isTimestampFresh(prepare.Timestamp) {
			return fmt.Errorf("stale prepare in proof: %s", prepare.BuddyID)
		}

		if prepare.Decision == msg.Decision {
			supportingCount++
		}
	}

	if supportingCount < e.threshold {
		return fmt.Errorf("insufficient supporting votes")
	}

	return nil
}

func (e *engine) createCommit(decision Decision) (*CommitMessage, error) {
	proof := e.collectPrepareProof(decision)
	if len(proof) < e.threshold {
		return nil, fmt.Errorf("insufficient proof: have %d, need %d", len(proof), e.threshold)
	}
	if len(proof) > e.config.MaxProofSize {
		return nil, fmt.Errorf("proof too large")
	}

	msg := &CommitMessage{
		Version:      CommitVersionV1,
		Seq:          e.ensureSeqForSelf(),
		Round:        e.round,
		BlockHash:    e.blockHash,
		BuddyID:      e.myBuddyID,
		Decision:     decision,
		PrepareProof: proof,
		Timestamp:    time.Now().Unix(),
	}

	return msg, nil
}

func (e *engine) checkAndMarkSeq(buddyID string, seq uint64) error {
	e.seqMu.Lock()
	defer e.seqMu.Unlock()

	last := e.lastSeqSeen[buddyID]
	if seq <= last {
		return fmt.Errorf("sequence not monotonic: got=%d last=%d", seq, last)
	}
	e.lastSeqSeen[buddyID] = seq
	return nil
}

func (e *engine) ensureSeqForSelf() uint64 {
	e.seqMu.Lock()
	defer e.seqMu.Unlock()
	last := e.lastSeqSeen[e.myBuddyID]
	last++
	e.lastSeqSeen[e.myBuddyID] = last
	return last
}

func isTimestampFresh(ts int64) bool {
	now := time.Now().Unix()
	if ts == 0 {
		return false
	}
	diff := now - ts
	if diff < 0 {
		diff = -diff
	}
	return diff <= AllowedTimestampSkew
}
