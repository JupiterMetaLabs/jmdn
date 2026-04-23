package settings

import "time"

// AlertsConfig controls the external alerting/notification service.
// Prefer env vars for secrets: JMDN_ALERTS_API_KEY, JMDN_ALERTS_CHAT_ID
type AlertsConfig struct {
	URL         string        `mapstructure:"url"          yaml:"url"`
	APIKey      string        `mapstructure:"api_key"      yaml:"api_key"`
	ChatID      string        `mapstructure:"chat_id"      yaml:"chat_id"`
	HTTPTimeout time.Duration `mapstructure:"http_timeout" yaml:"http_timeout"`
}

// DefaultAlertsConfig returns a safe-by-default alerts configuration.
// All fields are empty — alerts are disabled until configured.
func DefaultAlertsConfig() AlertsConfig {
	return AlertsConfig{
		HTTPTimeout: 10 * time.Second,
	}
}
