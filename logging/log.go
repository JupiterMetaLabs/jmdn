// logging/log.go
package logging

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/grafana/loki-client-go/loki"
	"github.com/grafana/loki-client-go/pkg/backoff"
	"github.com/grafana/loki-client-go/pkg/urlutil"
	"github.com/prometheus/common/model"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	defaultLokiURL = "http://localhost:3100/loki/api/v1/push"
	// defaultLokiURL = "http://localhost:3100/loki/api/v1/push" // Temporarily disable Loki // Debugging
)

// getLokiURL returns the Loki URL from environment variable or default
func GetLokiURL() string {
	if url := os.Getenv("LOKI_URL"); url != "" {
		return url
	}
	return defaultLokiURL
}

// AsyncLogger is the handle returned by NewAsyncLogger.
// Use Logger for logging and Close() when shutting down.
type AsyncLogger struct {
	Logger    *zap.Logger
	file      *os.File
	fileAsync *asyncWriteSyncer
	lokiAsync *lokiWriteSyncer
	closeOnce sync.Once
	Logging   *Logging
}

func ReturnDefaultLogger(FileName string, Topic string) (*AsyncLogger, error) {
	const (
		TempLOG_DIR         = "logs"
		TempLOKI_BATCH_SIZE = 100
		TempLOKI_BATCH_WAIT = 2 * time.Second
		TempLOKI_TIMEOUT    = 6 * time.Second
		TempKEEP_LOGS       = true
	)
	return NewAsyncLogger(&Logging{
		FileName: FileName,
		URL:      GetLokiURL(),
		Metadata: LoggingMetadata{
			DIR:       TempLOG_DIR,
			BatchSize: TempLOKI_BATCH_SIZE,
			BatchWait: TempLOKI_BATCH_WAIT,
			Timeout:   TempLOKI_TIMEOUT,
			KeepLogs:  TempKEEP_LOGS,
		},
		Topic: Topic,
	})
}

// NewAsyncLogger creates a Zap logger with the specified configuration
func NewAsyncLogger(cfg *Logging) (*AsyncLogger, error) {
	if cfg == nil {
		return nil, fmt.Errorf("logging config cannot be nil")
	}

	// Set up log directory
	if err := os.MkdirAll(cfg.Metadata.DIR, 0755); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Open log file
	logFilePath := filepath.Join(cfg.Metadata.DIR, cfg.FileName)
	file, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	// Configure encoder
	encCfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	encoder := zapcore.NewJSONEncoder(encCfg)

	// File sink
	fileWS := zapcore.AddSync(file)
	fileAsync := newAsyncWriteSyncer(fileWS, 4096, 2*time.Second)
	fileCore := zapcore.NewCore(encoder, fileAsync, zap.DebugLevel)

	// Initialize cores slice with file core
	cores := []zapcore.Core{fileCore}

	// Loki sink if URL is provided
	var lokiAsync *lokiWriteSyncer
	if cfg.URL != "" {
		parsedURL, err := url.Parse(GetLokiURL())
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("failed to parse loki URL: %w", err)
		}

		lokiCfg := loki.Config{
			URL:       urlutil.URLValue{URL: parsedURL},
			BatchWait: cfg.Metadata.BatchWait,
			BatchSize: cfg.Metadata.BatchSize,
			BackoffConfig: backoff.BackoffConfig{
				MinBackoff: 100 * time.Millisecond,
				MaxBackoff: 1 * time.Second,
				MaxRetries: 2,
			},
			Timeout: 6 * time.Second,
		}

		lc, err := loki.New(lokiCfg)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("failed to create loki client: %w", err)
		}

		// Create labels with topic if provided
		labels := model.LabelSet{"job": "gossipnode", "app": "immudb"}
		if cfg.Topic != "" {
			labels["topic"] = model.LabelValue(cfg.Topic)
		}

		lokiAsync = newLokiWriteSyncer(lc, labels)
		lokiCore := zapcore.NewCore(encoder, lokiAsync, zap.DebugLevel)
		cores = append(cores, lokiCore)
	}

	// Create core with all sinks
	core := zapcore.NewTee(cores...)

	// Create logger with additional fields if topic is set
	var logger *zap.Logger
	if cfg.Topic != "" {
		logger = zap.New(core,
			zap.AddCaller(),
			zap.AddCallerSkip(1),
			zap.Fields(zap.String("topic", cfg.Topic)),
		)
	} else {
		logger = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	}

	al := &AsyncLogger{
		Logger:    logger,
		file:      file,
		fileAsync: fileAsync,
		lokiAsync: lokiAsync,
		Logging:   cfg,
	}

	// Log initialization
	logger.Info("Logger initialized",
		zap.String("log_file", logFilePath),
		zap.String("topic", cfg.Topic),
		zap.String("loki_url", cfg.URL),
	)

	return al, nil
}

// Close flushes and closes all writers and background goroutines.
func (a *AsyncLogger) Close() {
	a.closeOnce.Do(func() {
		_ = a.Logger.Sync() // best-effort (may return "invalid argument" on some OS)

		if a.fileAsync != nil {
			a.fileAsync.Close()
		}
		if a.lokiAsync != nil {
			a.lokiAsync.Close()
		}
		if a.file != nil {
			_ = a.file.Close()
		}
	})
}

// GetSubLogger returns a logger with an added "component" field.
func (a *AsyncLogger) GetSubLogger(component string) *zap.Logger {
	return a.Logger.With(zap.String("component", component))
}

// WithTopic returns a logger that always includes the given topic field.
func (a *AsyncLogger) WithTopic(topic string) *zap.Logger {
	return a.Logger.With(zap.String("topic", topic))
}

// GetLogger returns a logger scoped with both component and topic.
// Pass component or topic as "" if not needed.
func (a *AsyncLogger) GetLogger(component, topic string) *zap.Logger {
	l := a.Logger
	if component != "" {
		l = l.With(zap.String("component", component))
	}
	if topic != "" {
		l = l.With(zap.String("topic", topic))
	}
	return l
}

// FieldTopic provides a reusable field for ad-hoc logging with topic.
func FieldTopic(topic string) zap.Field {
	return zap.String("topic", topic)
}

// ---------- Helpers ----------

func getenvOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------- Async file writer (non-blocking) ----------

type asyncWriteSyncer struct {
	ws      zapcore.WriteSyncer
	ch      chan []byte
	wg      sync.WaitGroup
	closed  chan struct{}
	flushEv chan struct{}

	batchSize     int
	flushInterval time.Duration
}

func newAsyncWriteSyncer(ws zapcore.WriteSyncer, batchSize int, flushInterval time.Duration) *asyncWriteSyncer {
	a := &asyncWriteSyncer{
		ws:            ws,
		ch:            make(chan []byte, 8192),
		closed:        make(chan struct{}),
		flushEv:       make(chan struct{}, 1),
		batchSize:     batchSize,
		flushInterval: flushInterval,
	}
	a.wg.Add(1)
	go a.loop()
	return a
}

func (a *asyncWriteSyncer) Write(p []byte) (int, error) {
	// copy because zap reuses buffers
	cp := make([]byte, len(p))
	copy(cp, p)
	select {
	case a.ch <- cp:
	default:
		// channel full: drop oldest behavior
		select {
		case <-a.ch:
		default:
		}
		select {
		case a.ch <- cp:
		default:
			// drop current line if still saturated
		}
	}
	return len(p), nil
}

func (a *asyncWriteSyncer) Sync() error {
	// trigger flush
	select {
	case a.flushEv <- struct{}{}:
	default:
	}
	return nil
}

func (a *asyncWriteSyncer) loop() {
	defer a.wg.Done()

	t := time.NewTicker(a.flushInterval)
	defer t.Stop()

	buf := make([][]byte, 0, 256)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		for _, b := range buf {
			_, _ = a.ws.Write(b)
		}
		_ = a.ws.Sync()
		buf = buf[:0]
	}

	for {
		select {
		case <-a.closed:
			flush()
			return
		case <-t.C:
			flush()
		case <-a.flushEv:
			flush()
		case b := <-a.ch:
			if b == nil {
				continue
			}
			buf = append(buf, b)
			if len(buf) >= a.batchSize {
				flush()
			}
		}
	}
}

func (a *asyncWriteSyncer) Close() {
	close(a.closed)
	a.wg.Wait()
	_ = a.ws.Sync()
}
