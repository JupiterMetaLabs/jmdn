package Test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gossipnode/logging"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestLogger_WithLoki(t *testing.T) {
    // Setup test Loki server
    var receivedLogs int32
    testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/loki/api/v1/push" {
            atomic.AddInt32(&receivedLogs, 1)
            w.WriteHeader(http.StatusNoContent)
        }
    }))
    defer testServer.Close()

    // Setup test environment
    logDir := "test_logs"
    logFilePath := filepath.Join(logDir, "test.log")
    
    cleanup := func() {
        if os.Getenv("KEEP_LOGS") != "true" {
            os.RemoveAll(logDir)
        }
    }
    defer cleanup()

    // Create logger config
    cfg := &logging.Logging{
        FileName: "test.log",
        Topic:    "test_topic",
        URL:      testServer.URL + "/loki/api/v1/push",
        Metadata: logging.LoggingMetadata{
            DIR:       logDir,
            BatchSize: 128 * 1024,
            BatchWait: 2 * time.Second,
            Timeout:   10 * time.Second,
        },
    }

    // Create logger
    logger, err := logging.NewAsyncLogger(cfg)
    require.NoError(t, err)
    defer logger.Close()

    // Test logging
    const testMessage = "test loki message"
    logger.Logger.Info(testMessage, 
        zap.String("test", "loki_integration"),
        zap.Int("count", 42),
    )

    // Give time for async writes
    time.Sleep(500 * time.Millisecond)
    logger.Close() // Force flush

    // Verify file logging
    file, err := os.Open(logFilePath)
    require.NoError(t, err)
    defer file.Close()

    var found bool
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        var entry map[string]interface{}
        if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil {
            if msg, ok := entry["msg"].(string); ok && msg == testMessage {
                // Verify topic is set
                if topic, ok := entry["topic"].(string); ok {
                    assert.Equal(t, "test_topic", topic, "Log entry should have the correct topic")
                } else {
                    t.Error("Log entry is missing topic field")
                }
                found = true
                break
            }
        }
    }
    assert.True(t, found, "Log message should be in the log file")

    // Verify Loki received the logs
    assert.Greater(t, atomic.LoadInt32(&receivedLogs), int32(0), 
        "Loki server should have received log entries")
}

func TestLogger_StreamLogs(t *testing.T) {
    // Setup mock Loki server
    var receivedBatches int32
    testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/loki/api/v1/push" {
            atomic.AddInt32(&receivedBatches, 1)
            w.WriteHeader(http.StatusNoContent)
        }
    }))
    defer testServer.Close()

    // Setup test environment
    logDir := "test_perf_logs"
    logFileName := "perf_test.log"
    logFilePath := filepath.Join(logDir, logFileName)
    
    // Create logger config
    cfg := &logging.Logging{
        FileName: logFileName,
        Topic:    "performance",
        URL:      testServer.URL + "/loki/api/v1/push",
        Metadata: logging.LoggingMetadata{
            DIR:       logDir,
            BatchSize: 65536,
            BatchWait: 1 * time.Second,
            Timeout:   10 * time.Second,
        },
    }

    // Create logger
    logger, err := logging.NewAsyncLogger(cfg)
    require.NoError(t, err)
    defer logger.Close()

    // Channel to control the test
    stop := make(chan struct{})
    var wg sync.WaitGroup
    var count int64
    startTime := time.Now()

    // Start log producer
    wg.Add(1)
    go func() {
        defer wg.Done()
        ticker := time.NewTicker(time.Microsecond * 20) // ~50k TPS
        defer ticker.Stop()
        
        for i := 0; ; i++ {
            select {
            case <-ticker.C:
                logger.Logger.Info("high_perf_log", 
                    zap.Int64("count", atomic.AddInt64(&count, 1)),
                    zap.Int("worker", i%1000),
                    zap.String("data", strings.Repeat("x", 1024)),
                )
            case <-stop:
                return
            }
        }
    }()

    // Run for 10 seconds
    time.Sleep(10 * time.Second)
    close(stop)
    wg.Wait()
    logger.Close() // Ensure all logs are flushed

    // Calculate stats
    duration := time.Since(startTime).Seconds()
    tps := float64(atomic.LoadInt64(&count)) / duration

    // Output results
    t.Logf("Logged %d messages in %.2f seconds (%.2f TPS)", 
        atomic.LoadInt64(&count),
        duration,
        tps,
    )
    t.Logf("Received %d batches by Loki", atomic.LoadInt32(&receivedBatches))

    // Verify some logs were written to file
    file, err := os.Open(logFilePath)
    require.NoError(t, err)
    defer file.Close()

    var lineCount int
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        lineCount++
    }
    t.Logf("Wrote %d lines to log file", lineCount)
}