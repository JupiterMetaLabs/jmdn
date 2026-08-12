package settings

import "time"

// OrchestratorConfig points this node at the JMDT sequencer-orchestrator's
// callback API, used to report that a block THIS node proposed was rejected by
// the consensus committee.
//
// Why the callback exists: the orchestrator marks a batch's transactions
// "included" as soon as /api/process-block returns, but that handler answers
// before any vote is requested (Consensus.Start only spawns the voting
// goroutine). Without a report back, a block the committee votes down leaves
// its transactions permanently mislabelled as included in the orchestrator's
// failed-transaction table.
//
// Disabled by default: with URL or APIKey empty the reporter silently no-ops,
// exactly like AlertsConfig. Reporting is diagnostics — it must never be able
// to block or fail block production.
//
// Prefer env vars for the secret: JMDN_ORCHESTRATOR_API_KEY
type OrchestratorConfig struct {
	// URL is the full callback endpoint, e.g.
	// "http://127.0.0.1:8092/api/block/consensus-rejected".
	URL string `mapstructure:"url" yaml:"url"`

	// APIKey is sent as X-API-Key and must match the orchestrator's
	// CONSENSUS_CALLBACK_API_KEY. The orchestrator refuses the report (and
	// disables the route entirely) when its own key is unset.
	APIKey string `mapstructure:"api_key" yaml:"api_key"`

	// HTTPTimeout bounds a single POST attempt.
	HTTPTimeout time.Duration `mapstructure:"http_timeout" yaml:"http_timeout"`

	// MaxAttempts is the total number of POST attempts per report (1 = no
	// retry). Retries matter because the report is fire-and-forget: a dropped
	// report leaves the orchestrator's table wrong until the transaction is
	// resubmitted.
	MaxAttempts int `mapstructure:"max_attempts" yaml:"max_attempts"`
}

// DefaultOrchestratorConfig returns a disabled-by-default reporter config.
// URL and APIKey are empty, so nothing is sent until an operator configures it.
func DefaultOrchestratorConfig() OrchestratorConfig {
	return OrchestratorConfig{
		HTTPTimeout: 10 * time.Second,
		MaxAttempts: 3,
	}
}
