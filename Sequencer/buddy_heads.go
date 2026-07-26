package Sequencer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"gossipnode/config/settings"
)

// seedPeerHeadsPath is the seednode web-API endpoint that returns every peer's
// most recently reported block head (populated from ReportBlockState). It is a
// read-only JSON endpoint (bare array of peer summaries).
const seedPeerHeadsPath = "/api/peers"

// maxSeedPeersBody caps the best-effort peer-heads response read so a large or
// misbehaving seed response cannot balloon sequencer memory.
const maxSeedPeersBody = 4 << 20 // 4 MB

// seedHTTPBase returns the base URL of the seednode HTTP API, or "" when it
// cannot be determined (enrichment is then skipped). JMDN_SEED_HTTP_URL wins as
// an explicit override; otherwise the host of the gRPC seednode address is reused
// with the seednode web-API port (JMDN_SEED_HTTP_PORT, default 8080).
func seedHTTPBase() string {
	if u := strings.TrimSpace(os.Getenv("JMDN_SEED_HTTP_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	seed := ""
	if settings.IsLoaded() {
		seed = strings.TrimSpace(settings.Get().Network.SeedNode)
	}
	if seed == "" {
		return ""
	}
	// The gRPC seednode address is "host:port" (optionally scheme-prefixed);
	// reuse the host with the web-API port.
	host := strings.TrimPrefix(strings.TrimPrefix(seed, "http://"), "https://")
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if host == "" {
		return ""
	}
	port := strings.TrimSpace(os.Getenv("JMDN_SEED_HTTP_PORT"))
	if port == "" {
		port = "8080"
	}
	return "http://" + host + ":" + port
}

// fetchPeerHeads does a best-effort GET of the seednode's /api/peers endpoint and
// returns peer_id → latest reported block head. The caller supplies a ctx with a
// short timeout; any error is returned so the caller can degrade gracefully
// (render heights as unknown) rather than block or fail the consensus round.
func fetchPeerHeads(ctx context.Context, base string) (map[string]uint64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+seedPeerHeadsPath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("seed %s returned status %d", seedPeerHeadsPath, resp.StatusCode)
	}
	// The endpoint returns a bare JSON array of peer summaries; we only need the
	// peer id and its latest reported block.
	var summaries []struct {
		PeerID      string `json:"peerId"`
		LatestBlock uint64 `json:"latestBlock"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSeedPeersBody)).Decode(&summaries); err != nil {
		return nil, err
	}
	heads := make(map[string]uint64, len(summaries))
	for _, s := range summaries {
		if s.PeerID != "" {
			heads[s.PeerID] = s.LatestBlock
		}
	}
	return heads, nil
}
