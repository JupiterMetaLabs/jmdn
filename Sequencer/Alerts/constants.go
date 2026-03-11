package Alerts

import (
	"net/http"
	"sync"
	"time"

	"github.com/spf13/viper"

	"gossipnode/config/settings"
)

// Alert severity levels (used for both severity and status fields)
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
	SeveritySuccess = "success"
)

// Kept for backward compatibility — aliases to Severity* constants.
const (
	AlertStatusInfo    = SeverityInfo
	AlertStatusWarning = SeverityWarning
	AlertStatusError   = SeverityError
	AlertStatusSuccess = SeveritySuccess
)

// AlertQueueCapacity is the maximum number of alerts that can be queued asynchronously.
const AlertQueueCapacity = 64

var (
	singleton *alertService
	once      sync.Once
)

// Configure initializes the alert service lazily exactly once.
// It automatically reads the configuration from Viper settings.
// Alerts silently no-op if url or apiKey are empty.
func Configure() {
	once.Do(func() {
		// Read directly from Viper configuration
		var alertsConf settings.AlertsConfig
		if err := viper.UnmarshalKey("alerts", &alertsConf); err != nil {
			return
		}

		if alertsConf.URL == "" || alertsConf.APIKey == "" {
			return
		}

		httpTimeout := alertsConf.HTTPTimeout
		if httpTimeout == 0 {
			httpTimeout = 10 * time.Second
		}

		s := &alertService{
			url:    alertsConf.URL,
			apiKey: alertsConf.APIKey,
			chatID: alertsConf.ChatID,
			client: &http.Client{
				Timeout: httpTimeout,
			},
			queue: make(chan alertPayload, AlertQueueCapacity),
		}

		go s.drain()

		singleton = s
	})
}

// Enabled returns whether the alert service is configured and ready to send.
// It transparently initializes the service if this is the first call.
func Enabled() bool {
	Configure()
	return singleton != nil
}
