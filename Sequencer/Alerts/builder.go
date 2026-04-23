package Alerts

import (
	"context"
	"fmt"
)

// AlertBuilder provides a fluent interface for building and sending alerts.
type AlertBuilder struct {
	ctx         context.Context
	alertName   string
	status      string
	severity    string
	description string
	errorMsg    string
	labels      map[string]string
}

// NewAlertBuilder creates a new alert builder instance.
// If alerts are not configured, all methods are safe to call — Send() will no-op.
func NewAlertBuilder(ctx context.Context) *AlertBuilder {
	return &AlertBuilder{
		ctx: ctx,
	}
}

// Msg sets the alert message/error message.
func (b *AlertBuilder) Msg(msg string) *AlertBuilder {
	b.errorMsg = msg
	return b
}

// AlertName sets the alert name.
func (b *AlertBuilder) AlertName(name string) *AlertBuilder {
	b.alertName = name
	return b
}

// Description sets the alert description.
func (b *AlertBuilder) Description(desc string) *AlertBuilder {
	b.description = desc
	return b
}

// Severity sets the alert severity level.
func (b *AlertBuilder) Severity(sev string) *AlertBuilder {
	b.severity = sev
	return b
}

// Status sets the alert status (firing, resolved, etc.).
func (b *AlertBuilder) Status(status string) *AlertBuilder {
	b.status = status
	return b
}

// Label adds a single label to the alert.
func (b *AlertBuilder) Label(key, value string) *AlertBuilder {
	if b.labels == nil {
		b.labels = make(map[string]string)
	}
	b.labels[key] = value
	return b
}

// Labels merges multiple labels into the alert.
func (b *AlertBuilder) Labels(labels map[string]string) *AlertBuilder {
	if b.labels == nil {
		b.labels = make(map[string]string)
	}
	for k, v := range labels {
		b.labels[k] = v
	}
	return b
}

// Send enqueues the alert for async delivery.
// No-ops silently if the alert service is not configured.
func (b *AlertBuilder) Send() {
	if !Enabled() {
		return
	}

	if b.status == "" {
		b.status = SeverityError
	}
	if b.severity == "" {
		b.severity = SeverityError
	}

	desc := b.description
	if b.errorMsg != "" {
		if desc != "" {
			desc = fmt.Sprintf("%s: %s", desc, b.errorMsg)
		} else {
			desc = b.errorMsg
		}
	}
	if desc == "" {
		desc = "No description provided"
	}

	if b.labels == nil {
		b.labels = make(map[string]string)
	}

	singleton.enqueue(alertPayload{
		alertName:   b.alertName,
		status:      b.status,
		severity:    b.severity,
		description: desc,
		labels:      b.labels,
	})
}
