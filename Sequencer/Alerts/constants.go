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
	SeveritySuccess  = "success"
	SeverityUnknown  = "unknown"
	SeverityError    = "error"
)

// Alert Statuses
const (
	AlertStatusFiring   = "firing"
	AlertStatusResolved = "resolved"
	AlertStatusUnknown  = "unknown"
	AlertStatusInfo     = "info"
	AlertStatusError    = "error"
	AlertStatusWarning  = "warning"
)

// Alert names for consensus-related alerts
const ()
