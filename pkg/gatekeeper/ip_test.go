package gatekeeper

import (
	"net/http"
	"testing"

	"jmdn/config/settings"
)

func TestGetClientIPFromRequest_DirectIP(t *testing.T) {
	cfg := &settings.SecurityConfig{
		TrustForwardedHeaders: false,
	}
	ext := NewIPExtractor(cfg)

	req := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
	}
	got := ext.GetClientIPFromRequest(req)
	if got != "192.168.1.1" {
		t.Errorf("GetClientIPFromRequest = %q, want %q", got, "192.168.1.1")
	}
}

func TestGetClientIPFromRequest_IgnoresXFFWhenUntrusted(t *testing.T) {
	cfg := &settings.SecurityConfig{
		TrustForwardedHeaders: true,
		TrustedProxies:        []string{"10.0.0.0/8"}, // Only trust 10.x
	}
	ext := NewIPExtractor(cfg)

	req := &http.Request{
		RemoteAddr: "192.168.1.1:12345", // NOT a trusted proxy
		Header: http.Header{
			"X-Forwarded-For": []string{"1.2.3.4"},
		},
	}
	// Should return the direct IP because 192.168.1.1 is not in 10.0.0.0/8
	got := ext.GetClientIPFromRequest(req)
	if got != "192.168.1.1" {
		t.Errorf("expected direct IP, got %q", got)
	}
}

func TestGetClientIPFromRequest_TrustsXFFFromTrustedProxy(t *testing.T) {
	cfg := &settings.SecurityConfig{
		TrustForwardedHeaders: true,
		TrustedProxies:        []string{"10.0.0.0/8"},
	}
	ext := NewIPExtractor(cfg)

	req := &http.Request{
		RemoteAddr: "10.0.0.1:12345", // Trusted proxy
		Header: http.Header{
			"X-Forwarded-For": []string{"1.2.3.4, 10.0.0.1"},
		},
	}
	got := ext.GetClientIPFromRequest(req)
	if got != "1.2.3.4" {
		t.Errorf("expected forwarded IP %q, got %q", "1.2.3.4", got)
	}
}

func TestGetClientIPFromRequest_CloudflarePriority(t *testing.T) {
	cfg := &settings.SecurityConfig{
		TrustForwardedHeaders: true,
		TrustedProxies:        []string{"10.0.0.1"},
	}
	ext := NewIPExtractor(cfg)

	req := &http.Request{
		RemoteAddr: "10.0.0.1:12345",
		Header: http.Header{
			"Cf-Connecting-Ip": []string{"5.5.5.5"},
			"X-Forwarded-For":  []string{"6.6.6.6"},
			"X-Real-Ip":        []string{"7.7.7.7"},
		},
	}
	// Cloudflare header should take priority
	got := ext.GetClientIPFromRequest(req)
	if got != "5.5.5.5" {
		t.Errorf("expected CF IP %q, got %q", "5.5.5.5", got)
	}
}

func TestGetClientIPFromRequest_XRealIPFallback(t *testing.T) {
	cfg := &settings.SecurityConfig{
		TrustForwardedHeaders: true,
		TrustedProxies:        []string{"10.0.0.1"},
	}
	ext := NewIPExtractor(cfg)

	req := &http.Request{
		RemoteAddr: "10.0.0.1:12345",
		Header: http.Header{
			"X-Real-Ip": []string{"7.7.7.7"},
		},
	}
	got := ext.GetClientIPFromRequest(req)
	if got != "7.7.7.7" {
		t.Errorf("expected X-Real-IP %q, got %q", "7.7.7.7", got)
	}
}

func TestIsTrustedProxy_EmptyList(t *testing.T) {
	cfg := &settings.SecurityConfig{}
	ext := NewIPExtractor(cfg)
	if ext.IsTrustedProxy("10.0.0.1") {
		t.Error("expected IsTrustedProxy=false for empty list")
	}
}

func TestIsTrustedProxy_CIDRMatch(t *testing.T) {
	cfg := &settings.SecurityConfig{
		TrustedProxies: []string{"10.0.0.0/8"},
	}
	ext := NewIPExtractor(cfg)

	if !ext.IsTrustedProxy("10.255.0.1") {
		t.Error("expected 10.255.0.1 to match 10.0.0.0/8")
	}
	if ext.IsTrustedProxy("192.168.1.1") {
		t.Error("expected 192.168.1.1 to NOT match 10.0.0.0/8")
	}
}

func TestIsTrustedProxy_SingleIPMatch(t *testing.T) {
	cfg := &settings.SecurityConfig{
		TrustedProxies: []string{"10.0.0.1"}, // Single IP, not CIDR
	}
	ext := NewIPExtractor(cfg)

	if !ext.IsTrustedProxy("10.0.0.1") {
		t.Error("expected exact match for single IP")
	}
	if ext.IsTrustedProxy("10.0.0.2") {
		t.Error("expected no match for different IP")
	}
}

func TestIsTrustedProxy_InvalidIP(t *testing.T) {
	cfg := &settings.SecurityConfig{
		TrustedProxies: []string{"10.0.0.0/8"},
	}
	ext := NewIPExtractor(cfg)
	if ext.IsTrustedProxy("not-an-ip") {
		t.Error("expected false for invalid IP")
	}
}

func TestGetClientIPFromRequest_NoForwardedHeaders(t *testing.T) {
	cfg := &settings.SecurityConfig{
		TrustForwardedHeaders: true,
		TrustedProxies:        []string{"10.0.0.1"},
	}
	ext := NewIPExtractor(cfg)

	req := &http.Request{
		RemoteAddr: "10.0.0.1:12345",
		Header:     http.Header{}, // No forwarding headers
	}
	// Falls back to direct IP
	got := ext.GetClientIPFromRequest(req)
	if got != "10.0.0.1" {
		t.Errorf("expected direct IP when no forwarded headers, got %q", got)
	}
}
