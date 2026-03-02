package Alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// alertService handles sending alerts to the monitoring API
type alertService struct {
	url    string
	apiKey string
	chatID string
}

// newAlertService creates a new alert service instance
func newAlertService() *alertService {
	return &alertService{
		url:    AlertURL,
		apiKey: AlertAPIKey,
		chatID: ChatID,
	}
}

// sendAlert sends an alert to the monitoring API
func (s *alertService) sendAlert(
	ctx context.Context,
	alertName, status, severity, description, errorMsg string,
	labels map[string]string,
) {
	fullDescription := description
	if errorMsg != "" {
		fullDescription = fmt.Sprintf("%s: %s", description, errorMsg)
	}

	// Build labels map starting with required labels
	alertLabels := map[string]interface{}{
		"alertname": alertName,
		"severity":  severity,
	}

	// Merge optional labels
	for k, v := range labels {
		alertLabels[k] = v
	}

	alertPayload := map[string]interface{}{
		"chat_ids": []string{s.chatID},
		"alerts": []map[string]interface{}{
			{
				"status": status,
				"labels": alertLabels,
				"annotations": map[string]interface{}{
					"description": fullDescription,
				},
				"startsAt": time.Now().UTC().Format(time.RFC3339),
			},
		},
	}

	jsonData, err := json.Marshal(alertPayload)
	if err != nil {
		log.Printf("❌ [ALERT] Failed to marshal alert payload: %v", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("❌ [ALERT] Failed to create alert request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", s.apiKey)

	client := &http.Client{
		Timeout: HTTPTimeout,
	}

	log.Printf("🚨 [ALERT] Sending %s alert to %s", alertName, s.url)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("❌ [ALERT] Failed to send %s alert: %v", alertName, err)
		return
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("❌ [ALERT] Failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("✅ [ALERT] Successfully sent %s alert (status: %d)", alertName, resp.StatusCode)
	} else {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("⚠️  [ALERT] Alert API returned non-success status %d: %s", resp.StatusCode, string(body))
	}
}
