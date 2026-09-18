package reputation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Store persistence (D-62). The store is in-memory and resets on every sequencer
// restart — so a restart snaps EVERY peer back to Start (0.50 → weight 0.70) and
// the next absolute push overwrites the seed with that reset. That is how the
// operator's manual weight recovery kept getting clobbered, and why a restart was
// the ONLY way out of the 2026-09-17 spiral. Persisting the store across restarts
// makes the pushed weights stable and lets an operator's fix survive.
//
// Format: a small JSON of {peerID: {score, last_unix}}. Best-effort and
// self-contained (no external store) — a corrupt/missing file loads as empty,
// exactly the current behaviour, so this is strictly safer than today.

type persistedEntry struct {
	Score    float64 `json:"score"`
	LastUnix int64   `json:"last_unix"`
}

// Save writes the store's scores to path atomically (temp + rename).
func (s *Store) Save(path string) error {
	s.mu.Lock()
	out := make(map[string]persistedEntry, len(s.scores))
	for id, e := range s.scores {
		out[id] = persistedEntry{Score: e.score, LastUnix: e.last.Unix()}
	}
	s.mu.Unlock()

	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load replaces the store's contents from path. A missing file is not an error
// (returns nil, leaving the store empty) — the observe-only cold-start behaviour.
func (s *Store) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var in map[string]persistedEntry
	if err := json.Unmarshal(data, &in); err != nil {
		return err // corrupt file: surface it; caller may choose to ignore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scores = make(map[string]entry, len(in))
	for id, pe := range in {
		s.scores[id] = entry{score: clamp(pe.Score), last: time.Unix(pe.LastUnix, 0)}
	}
	return nil
}
