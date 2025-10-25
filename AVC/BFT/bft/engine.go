// =============================================================================
// FILE: pkg/bft/engine.go
// =============================================================================
package bft

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type engine struct {
	config     Config
	myBuddyID  string
	myDecision Decision
	privateKey []byte
	buddies    map[string]*BuddyInput
	threshold  int
	round      uint64
	blockHash  string

	byzantine *byzantineDetector

	mu          sync.RWMutex
	prepareMsgs map[string]*PrepareMessage
	commitMsgs  map[string]*CommitMessage

	// signer abstraction (local)
	signer Signer

	seqMu       sync.RWMutex
	lastSeqSeen map[string]uint64
}

func (e *engine) runPrepare(ctx context.Context, messenger Messenger) (*PhaseResult, error) {
	myPrepare := &PrepareMessage{
		Version:   "PREPARE_V1",
		Seq:       ensureSeqForSelf(e),
		Round:     e.round,
		BlockHash: e.blockHash,
		BuddyID:   e.myBuddyID,
		Decision:  e.myDecision,
		Timestamp: time.Now().Unix(),
	}

	if err := e.signPrepare(myPrepare); err != nil {
		return nil, err
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
			return "", fmt.Errorf("COMMIT timeout: accepts=%d, rejects=%d, need=%d, byzantine=%d", acceptCount, rejectCount, e.threshold, e.byzantine.count())
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

func (e *engine) signPrepare(msg *PrepareMessage) error {
	payload, _ := json.Marshal(struct {
		Version   string
		Round     uint64
		BlockHash string
		BuddyID   string
		Decision  Decision
		Seq       uint64
		Timestamp int64
	}{
		Version:   msg.Version,
		Round:     msg.Round,
		BlockHash: msg.BlockHash,
		BuddyID:   msg.BuddyID,
		Decision:  msg.Decision,
		Seq:       msg.Seq,
		Timestamp: msg.Timestamp,
	})

	if e.signer == nil {
		msg.Signature = ed25519.Sign(ed25519.PrivateKey(e.privateKey), payload)
		return nil
	}
	sig, err := e.signer.Sign(payload)
	if err != nil {
		return err
	}
	msg.Signature = sig
	return nil
}

func (e *engine) signCommit(msg *CommitMessage) error {
	payload, _ := json.Marshal(struct {
		Version   string
		Round     uint64
		BlockHash string
		BuddyID   string
		Decision  Decision
		ProofSize int
		Seq       uint64
		Timestamp int64
	}{
		Version:   msg.Version,
		Round:     msg.Round,
		BlockHash: msg.BlockHash,
		BuddyID:   msg.BuddyID,
		Decision:  msg.Decision,
		ProofSize: len(msg.PrepareProof),
		Seq:       msg.Seq,
		Timestamp: msg.Timestamp,
	})

	if e.signer == nil {
		msg.Signature = ed25519.Sign(ed25519.PrivateKey(e.privateKey), payload)
		return nil
	}
	sig, err := e.signer.Sign(payload)
	if err != nil {
		return err
	}
	msg.Signature = sig
	return nil
}

func (e *engine) validatePrepare(msg *PrepareMessage) error {
	buddy, exists := e.buddies[msg.BuddyID]
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

	if e.config.RequireSignatures {
		payload, _ := json.Marshal(struct {
			Version   string
			Round     uint64
			BlockHash string
			BuddyID   string
			Decision  Decision
			Seq       uint64
			Timestamp int64
		}{msg.Version, msg.Round, msg.BlockHash, msg.BuddyID, msg.Decision, msg.Seq, msg.Timestamp})

		if !ed25519.Verify(ed25519.PublicKey(buddy.PublicKey), payload, msg.Signature) {
			return fmt.Errorf("invalid signature")
		}
	}

	return nil
}

func (e *engine) validateCommit(msg *CommitMessage) error {
	buddy, exists := e.buddies[msg.BuddyID]
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

	if e.config.ValidateProofs {
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

			payload, _ := json.Marshal(struct {
				Version   string
				Round     uint64
				BlockHash string
				BuddyID   string
				Decision  Decision
				Seq       uint64
				Timestamp int64
			}{prepare.Version, prepare.Round, prepare.BlockHash, prepare.BuddyID, prepare.Decision, prepare.Seq, prepare.Timestamp})

			pubBytes := e.buddies[prepare.BuddyID].PublicKey
			if !ed25519.Verify(ed25519.PublicKey(pubBytes), payload, prepare.Signature) {
				return fmt.Errorf("invalid signature in proof from %s", prepare.BuddyID)
			}

			if prepare.Decision == msg.Decision {
				supportingCount++
			}
		}

		if supportingCount < e.threshold {
			return fmt.Errorf("insufficient supporting votes")
		}
	}

	if e.config.RequireSignatures {
		payload, _ := json.Marshal(struct {
			Version   string
			Round     uint64
			BlockHash string
			BuddyID   string
			Decision  Decision
			ProofSize int
			Seq       uint64
			Timestamp int64
		}{msg.Version, msg.Round, msg.BlockHash, msg.BuddyID, msg.Decision, len(msg.PrepareProof), msg.Seq, msg.Timestamp})

		if !ed25519.Verify(ed25519.PublicKey(buddy.PublicKey), payload, msg.Signature) {
			return fmt.Errorf("invalid signature")
		}
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
		Version:      "COMMIT_V1",
		Seq:          ensureSeqForSelf(e),
		Round:        e.round,
		BlockHash:    e.blockHash,
		BuddyID:      e.myBuddyID,
		Decision:     decision,
		PrepareProof: proof,
		Timestamp:    time.Now().Unix(),
	}

	if err := e.signCommit(msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// replay helpers
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

func ensureSeqForSelf(e *engine) uint64 {
	e.seqMu.Lock()
	defer e.seqMu.Unlock()
	last := e.lastSeqSeen[e.myBuddyID]
	last++
	e.lastSeqSeen[e.myBuddyID] = last
	return last
}

// global helper for freshness
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

// verify functions used by messenger (use registry)
func verifyPrepareSignature(msg *PrepareMessage) error {
	pub, ok := GetPublicKey(msg.BuddyID)
	if !ok || len(pub) == 0 {
		return fmt.Errorf("no public key for %s", msg.BuddyID)
	}

	payload, _ := json.Marshal(struct {
		Version   string
		Round     uint64
		BlockHash string
		BuddyID   string
		Decision  Decision
		Seq       uint64
		Timestamp int64
	}{msg.Version, msg.Round, msg.BlockHash, msg.BuddyID, msg.Decision, msg.Seq, msg.Timestamp})

	if !ed25519.Verify(ed25519.PublicKey(pub), payload, msg.Signature) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func verifyCommitSignature(msg *CommitMessage) error {
	pub, ok := GetPublicKey(msg.BuddyID)
	if !ok || len(pub) == 0 {
		return fmt.Errorf("no public key for %s", msg.BuddyID)
	}
	payload, _ := json.Marshal(struct {
		Version   string
		Round     uint64
		BlockHash string
		BuddyID   string
		Decision  Decision
		ProofSize int
		Seq       uint64
		Timestamp int64
	}{msg.Version, msg.Round, msg.BlockHash, msg.BuddyID, msg.Decision, len(msg.PrepareProof), msg.Seq, msg.Timestamp})

	if !ed25519.Verify(ed25519.PublicKey(pub), payload, msg.Signature) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}
