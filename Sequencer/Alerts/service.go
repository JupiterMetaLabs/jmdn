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

// AlertService handles sending alerts to the monitoring API
type AlertService struct {
	url    string
	apiKey string
	chatID string
}

// NewAlertService creates a new alert service instance
func NewAlertService() *AlertService {
	return &AlertService{
		url:    AlertURL,
		apiKey: AlertAPIKey,
		chatID: ChatID,
	}
}

// SendAlert sends an alert to the monitoring API
func (s *AlertService) SendAlert(
	ctx context.Context,
	alertName, description, severity, errorMsg string,
) {
	fullDescription := description
	if errorMsg != "" {
		fullDescription = fmt.Sprintf("%s: %s", description, errorMsg)
	}

	alertPayload := map[string]interface{}{
		"chat_ids": []string{s.chatID},
		"alerts": []map[string]interface{}{
			{
				"status": "firing",
				"labels": map[string]interface{}{
					"alertname": alertName,
					"severity":  severity,
				},
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
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("✅ [ALERT] Successfully sent %s alert (status: %d)", alertName, resp.StatusCode)
	} else {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("⚠️  [ALERT] Alert API returned non-success status %d: %s", resp.StatusCode, string(body))
	}
}
