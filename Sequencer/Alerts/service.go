package Alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

// alertPayload is the internal representation pushed onto the async queue.
type alertPayload struct {
	alertName   string
	status      string
	severity    string
	description string
	labels      map[string]string
}

// alertService handles sending alerts to the monitoring API.
// A single instance is created at Configure() time and reused.
type alertService struct {
	url    string
	apiKey string
	chatID string
	client *http.Client // reused across all sends — Go's http.Client is concurrency-safe
	queue  chan alertPayload
}

// enqueue pushes an alert onto the async send queue.
// Non-blocking: if the queue is full, the alert is dropped with a log warning.
func (s *alertService) enqueue(p alertPayload) {
	select {
	case s.queue <- p:
	default:
		log.Printf("[ALERT] Queue full, dropping alert: %s", p.alertName)
	}
}

// drain runs in a background goroutine, sending alerts from the queue.
func (s *alertService) drain() {
	for p := range s.queue {
		s.send(p)
	}
}

// send performs the HTTP POST to the alert endpoint.
func (s *alertService) send(p alertPayload) {
	alertLabels := map[string]interface{}{
		"alertname": p.alertName,
		"severity":  p.severity,
	}
	for k, v := range p.labels {
		alertLabels[k] = v
	}

	body := map[string]interface{}{
		"alerts": []map[string]interface{}{
			{
				"status": p.status,
				"labels": alertLabels,
				"annotations": map[string]interface{}{
					"description": p.description,
				},
				"startsAt": time.Now().UTC().Format(time.RFC3339),
			},
		},
	}

	if s.chatID != "" {
		body["chat_ids"] = []string{s.chatID}
	}

	jsonData, err := json.Marshal(body)
	if err != nil {
		log.Printf("[ALERT] Failed to marshal payload: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", s.url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[ALERT] Failed to create request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("[ALERT] Failed to send %s: %v", p.alertName, err)
		return
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("❌ [ALERT] Failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ALERT] Non-success response %d for %s: %s", resp.StatusCode, p.alertName, string(respBody))
	}
}
