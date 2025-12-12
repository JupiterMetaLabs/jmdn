package Alerts

import (
	"context"
)

// AlertBuilder provides a fluent interface for building and sending alerts
type AlertBuilder struct {
	ctx         context.Context
	alertName   string
	status      string
	severity    string
	description string
	errorMsg    string
	labels      map[string]string
	service     *alertService
}

// NewAlertBuilder creates a new alert builder instance
func NewAlertBuilder(ctx context.Context) *AlertBuilder {
	return &AlertBuilder{
		ctx:     ctx,
		service: newAlertService(),
	}
}

// Msg sets the alert message/error message
// Usage: NewAlertBuilder(ctx).Msg("error occurred").Success()
func (b *AlertBuilder) Msg(msg string) *AlertBuilder {
	b.errorMsg = msg
	return b
}

// AlertName sets the alert name
func (b *AlertBuilder) AlertName(name string) *AlertBuilder {
	b.alertName = name
	return b
}

// Description sets the alert description
func (b *AlertBuilder) Description(desc string) *AlertBuilder {
	b.description = desc
	return b
}

// Severity sets the alert severity level
func (b *AlertBuilder) Severity(sev string) *AlertBuilder {
	b.severity = sev
	return b
}

// Status sets the alert status (firing, resolved, etc.)
func (b *AlertBuilder) Status(status string) *AlertBuilder {
	b.status = status
	return b
}

// Label adds a single label to the alert
func (b *AlertBuilder) Label(key, value string) *AlertBuilder {
	if b.labels == nil {
		b.labels = make(map[string]string)
	}
	b.labels[key] = value
	return b
}

// Labels sets multiple labels for the alert (replaces existing labels)
func (b *AlertBuilder) Labels(labels map[string]string) *AlertBuilder {
	if b.labels == nil {
		b.labels = make(map[string]string)
	}
	for k, v := range labels {
		b.labels[k] = v
	}
	return b
}

// doSend sends the alert with default status if not set
func (b *AlertBuilder) doSend() {
	if b.status == "" {
		b.status = AlertStatusError
	}
	if b.severity == "" {
		b.severity = SeverityError
	}
	if b.description == "" || b.errorMsg == "" {
		b.description = "No description or error message provided"
	}
	if b.labels == nil {
		b.labels = make(map[string]string)
	}
	b.service.sendAlert(
		b.ctx,
		b.alertName,
		b.status,
		b.severity,
		b.description,
		b.errorMsg,
		b.labels,
	)
}

// Send sends the alert with the configured values
func (b *AlertBuilder) Send() {
	b.doSend()
}
