package Alerts

import (
	"context"
	"fmt"
)

// ConsensusAlertHandlers provides methods for sending consensus-related alerts
type ConsensusAlertHandlers struct {
	service *AlertService
}

// NewConsensusAlertHandlers creates a new consensus alert handlers instance
func NewConsensusAlertHandlers() *ConsensusAlertHandlers {
	return &ConsensusAlertHandlers{
		service: NewAlertService(),
	}
}

// SendConsensusReachedAccept sends an alert when consensus is reached with ACCEPT decision
func (h *ConsensusAlertHandlers) SendConsensusReachedAccept(
	ctx context.Context,
	blockHash string,
	acceptVotes, rejectVotes, totalVotes int,
) {
	msg := fmt.Sprintf("Block %s accepted. Votes: %d ACCEPT, %d REJECT (Total: %d)",
		blockHash, acceptVotes, rejectVotes, totalVotes)
	h.service.SendAlert(ctx, AlertConsensusReachedAccept,
		"Consensus reached with ACCEPT decision", SeverityInfo, msg)
}

// SendConsensusReachedReject sends an alert when consensus is reached with REJECT decision
func (h *ConsensusAlertHandlers) SendConsensusReachedReject(
	ctx context.Context,
	blockHash string,
	acceptVotes, rejectVotes, totalVotes int,
) {
	msg := fmt.Sprintf("Block %s rejected. Votes: %d ACCEPT, %d REJECT (Total: %d)",
		blockHash, acceptVotes, rejectVotes, totalVotes)
	h.service.SendAlert(ctx, AlertConsensusReachedReject,
		"Consensus reached with REJECT decision", SeverityWarning, msg)
}

// SendConsensusFailed sends an alert when consensus fails
func (h *ConsensusAlertHandlers) SendConsensusFailed(
	ctx context.Context,
	errorMsg string,
) {
	h.service.SendAlert(ctx, AlertConsensusFailed,
		"Consensus failed", SeverityCritical, errorMsg)
}

// SendInsufficientParticipation sends an alert when there's insufficient peer participation
func (h *ConsensusAlertHandlers) SendInsufficientParticipation(
	ctx context.Context,
	participated, required int,
	blockHash string,
) {
	msg := fmt.Sprintf("Block %s: Only %d buddies participated, need at least %d",
		blockHash, participated, required)
	h.service.SendAlert(ctx, AlertConsensusInsufficientPeers,
		"Insufficient peer participation for consensus", SeverityCritical, msg)
}

// SendBLSFailure sends an alert when BLS signature verification fails
func (h *ConsensusAlertHandlers) SendBLSFailure(
	ctx context.Context,
	blockHash string,
	reason string,
) {
	msg := fmt.Sprintf("Block %s: %s", blockHash, reason)
	h.service.SendAlert(ctx, AlertConsensusBLSFailure,
		"BLS signature verification failed", SeverityCritical, msg)
}

// SendConsensusTimeout sends an alert when consensus times out
func (h *ConsensusAlertHandlers) SendConsensusTimeout(
	ctx context.Context,
	blockHash string,
) {
	msg := fmt.Sprintf("Block %s: Consensus timed out", blockHash)
	h.service.SendAlert(ctx, AlertConsensusTimeout,
		"Consensus timeout", SeverityCritical, msg)
}

// SendInitFailure sends an alert when consensus initialization fails
func (h *ConsensusAlertHandlers) SendInitFailure(
	ctx context.Context,
	errorMsg string,
) {
	h.service.SendAlert(ctx, AlertConsensusInitFailure,
		"Consensus initialization failed", SeverityCritical, errorMsg)
}

// SendSubscriptionFailure sends an alert when subscription to consensus channel fails
func (h *ConsensusAlertHandlers) SendSubscriptionFailure(
	ctx context.Context,
	errorMsg string,
) {
	h.service.SendAlert(ctx, AlertConsensusSubscriptionFailure,
		"Failed to subscribe to consensus channel", SeverityCritical, errorMsg)
}
