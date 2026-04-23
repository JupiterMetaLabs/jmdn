package main

import (
	"testing"

	"gossipnode/config/settings"
)

func TestBuildThebeEventsConfigMapsFields(t *testing.T) {
	cfg := settings.ThebeConfig{
		RedisURL:   "redis://localhost:6379",
		StreamName: "jmdt.thebedb.events",
		MaxLen:     2048,
		GroupName:  "projector",
	}

	eventsCfg := buildThebeEventsConfig(cfg)
	if eventsCfg == nil {
		t.Fatal("expected non-nil events config")
	}
	if eventsCfg.RedisURL != cfg.RedisURL {
		t.Fatalf("redis url mismatch: got %q", eventsCfg.RedisURL)
	}
	if eventsCfg.StreamName != cfg.StreamName {
		t.Fatalf("stream name mismatch: got %q", eventsCfg.StreamName)
	}
	if eventsCfg.MaxLen != cfg.MaxLen {
		t.Fatalf("max len mismatch: got %d", eventsCfg.MaxLen)
	}
	if eventsCfg.GroupName != cfg.GroupName {
		t.Fatalf("group name mismatch: got %q", eventsCfg.GroupName)
	}
}

func TestBuildThebeEventsConfigNilWithoutRedisURL(t *testing.T) {
	cfg := settings.ThebeConfig{
		RedisURL:   "   ",
		StreamName: "jmdt.thebedb.events",
		MaxLen:     1000,
		GroupName:  "projector",
	}

	if got := buildThebeEventsConfig(cfg); got != nil {
		t.Fatal("expected nil events config when redis url is empty")
	}
}
