package otelsetup

import (
	"context"
	"path/filepath"
	"sync"

	"gossipnode/config/settings"
	"gossipnode/config/version"

	"github.com/JupiterMetaLabs/ion"
)

var (
	initOnce sync.Once
	// originalLogDir  string // unused: declared but never assigned or read
	// originalLogFile string // unused: declared but never assigned or read
	globalLogger   *ion.Ion
	globalWarnings []ion.Warning
	globalInitErr  error
)

// Setup initializes Ion with logging configured via the unified settings package.
// logDir/logFileName are fallback values for file logging; settings.Logging.File
// takes precedence when explicitly configured.
// Returns the initialized Ion instance and any warnings.
// Note: This function uses sync.Once to ensure initialization only happens once.
func Setup(logDir string, logFileName string) (*ion.Ion, []ion.Warning, error) {
	initOnce.Do(func() {
		logCfg := settings.Get().Logging

		// Build Ion configuration from unified settings
		cfg := ion.Default()

		cfg.ServiceName = logCfg.ServiceName
		cfg.Version = version.GetVersionInfo().GitTag
		cfg.Development = logCfg.Development
		cfg.Level = logCfg.Level

		// Console
		cfg.Console = ion.ConsoleConfig{
			Enabled:        logCfg.Console.Enabled,
			Format:         logCfg.Console.Format,
			Color:          logCfg.Console.Color,
			ErrorsToStderr: logCfg.Console.ErrorsToStderr,
		}

		// File — prefer settings; fall back to function args
		if logCfg.File.Enabled && logCfg.File.Path != "" {
			cfg.File = ion.FileConfig{
				Enabled:    true,
				Path:       logCfg.File.Path,
				MaxSizeMB:  logCfg.File.MaxSizeMB,
				MaxAgeDays: logCfg.File.MaxAgeDays,
				MaxBackups: logCfg.File.MaxBackups,
				Compress:   logCfg.File.Compress,
			}
		} else if logDir != "" && logFileName != "" {
			cfg.File = ion.FileConfig{
				Enabled: true,
				Path:    filepath.Join(logDir, logFileName),
			}
		} else {
			cfg.File = ion.FileConfig{Enabled: false}
		}

		// OTEL export
		if logCfg.OTEL.Enabled && logCfg.OTEL.Endpoint != "" {
			cfg.OTEL = ion.OTELConfig{
				Enabled:        true,
				Endpoint:       logCfg.OTEL.Endpoint,
				Protocol:       logCfg.OTEL.Protocol,
				Insecure:       logCfg.OTEL.Insecure,
				Username:       logCfg.OTEL.Username,
				Password:       logCfg.OTEL.Password,
				BatchSize:      logCfg.OTEL.BatchSize,
				ExportInterval: logCfg.OTEL.ExportInterval,
			}

			// Tracing (inherits OTEL endpoint)
			cfg.Tracing = ion.TracingConfig{
				Enabled: logCfg.Tracing.Enabled,
				Sampler: logCfg.Tracing.Sampler,
			}
		}

		// Initialize Ion
		globalLogger, globalWarnings, globalInitErr = ion.New(cfg)
	})

	return globalLogger, globalWarnings, globalInitErr
}

// Shutdown gracefully shuts down Ion, flushing all pending logs and traces
func Shutdown(ctx context.Context, ionInstance *ion.Ion) error {
	if ionInstance == nil {
		return nil
	}
	return ionInstance.Shutdown(ctx)
}
