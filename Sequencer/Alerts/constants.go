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
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
	SeveritySuccess = "success"
)

// Alert Statuses
const (
	AlertStatusInfo    = "info"
	AlertStatusWarning = "warning"
	AlertStatusError   = "error"
	AlertStatusSuccess = "success"
)

// Alert names for consensus-related alerts
const ()
