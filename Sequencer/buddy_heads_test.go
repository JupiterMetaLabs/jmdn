package Sequencer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchPeerHeads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != seedPeerHeadsPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"peerId":"12D3KooWpeerA","latestBlock":100,"rootStatus":"match"},
			{"peerId":"12D3KooWpeerB","latestBlock":98},
			{"peerId":"","latestBlock":5}
		]`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	heads, err := fetchPeerHeads(ctx, srv.URL)
	if err != nil {
		t.Fatalf("fetchPeerHeads: %v", err)
	}
	if heads["12D3KooWpeerA"] != 100 || heads["12D3KooWpeerB"] != 98 {
		t.Fatalf("unexpected heads: %+v", heads)
	}
	if _, ok := heads[""]; ok {
		t.Fatalf("empty peerId should be skipped")
	}
}

func TestFetchPeerHeads_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := fetchPeerHeads(ctx, srv.URL); err == nil {
		t.Fatal("expected error on non-200 response")
	}
}

func TestSeedHTTPBase_EnvOverrideWins(t *testing.T) {
	t.Setenv("JMDN_SEED_HTTP_URL", "http://seed.example:9000/")
	if got := seedHTTPBase(); got != "http://seed.example:9000" {
		t.Fatalf("seedHTTPBase() = %q, want http://seed.example:9000 (trailing slash trimmed)", got)
	}
}
