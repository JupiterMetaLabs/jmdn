package Alerts

import "time"

// Alert configuration constants
const (
	AlertURL    = "https://tg.jmdt.io/multi-channel"
	AlertAPIKey = "jmdt-node-alerts"
	ChatID      = "-1003376278459"
	HTTPTimeout = 10 * time.Second
)

// Alert severity levels
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Alert names for consensus-related alerts
const (
	AlertConsensusReachedAccept       = "Consensus Reached (ACCEPT)"
	AlertConsensusReachedReject       = "Consensus Reached (REJECT)"
	AlertConsensusFailed              = "Consensus Failed"
	AlertConsensusInsufficientPeers   = "Consensus Insufficient Participation"
	AlertConsensusBLSFailure          = "Consensus BLS Signature Failure"
	AlertConsensusTimeout             = "Consensus Timeout"
	AlertConsensusInitFailure         = "Consensus Initialization Failed"
	AlertConsensusSubscriptionFailure = "Consensus Subscription Failed"
)
