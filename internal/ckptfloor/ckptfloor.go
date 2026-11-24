// Package ckptfloor keeps the chain-head checkpoint floor: the highest
// committee-signed checkpoint this node has already verified or created.
//
// The floor lives in a small file OUTSIDE the ThebeDB store it protects. At
// boot the latest stored checkpoint must be at or above it, so deleting the
// __ckpt:* keys or restoring an older copy of the store (a rollback) is caught
// instead of silently passing. A floor stored inside the same database would be
// rolled back together with it.
package ckptfloor

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Floor is the persisted record.
type Floor struct {
	ChainID uint64 `json:"chain_id"`
	Seq     uint64 `json:"seq"`  // canonical-log sequence of the checkpoint
	Head    string `json:"head"` // hex chain head of that checkpoint
}

// HeadBytes decodes Head; nil when empty or malformed.
func (f Floor) HeadBytes() []byte {
	b, err := hex.DecodeString(f.Head)
	if err != nil || len(b) == 0 {
		return nil
	}
	return b
}

// Load reads the floor at path. ok=false when the file does not exist (no floor
// yet). A file that exists but cannot be read or parsed is an error: treating
// it as "no floor" would let a corrupted file disable the check.
func Load(path string) (f Floor, ok bool, err error) {
	if path == "" {
		return Floor{}, false, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Floor{}, false, nil
	}
	if err != nil {
		return Floor{}, false, fmt.Errorf("ckptfloor: read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return Floor{}, false, fmt.Errorf("ckptfloor: parse %s: %w", path, err)
	}
	// An unreadable head would turn off the head comparison at boot, so it is
	// rejected the same way as an unparsable file.
	if f.Head != "" && f.HeadBytes() == nil {
		return Floor{}, false, fmt.Errorf("ckptfloor: %s: head %q is not hex", path, f.Head)
	}
	return f, true, nil
}

// Raise stores (chainID, seq, head) at path if seq is above the current floor
// (or no floor exists). It never lowers the floor. The write is atomic and
// fsync'd: temp file in the same directory, fsync, rename, fsync the directory.
func Raise(path string, chainID, seq uint64, head []byte) error {
	if path == "" {
		return nil
	}
	cur, ok, err := Load(path)
	if err != nil {
		return err
	}
	if ok && cur.Seq >= seq {
		return nil
	}
	b, err := json.Marshal(Floor{ChainID: chainID, Seq: seq, Head: hex.EncodeToString(head)})
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("ckptfloor: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".ckptfloor-*")
	if err != nil {
		return fmt.Errorf("ckptfloor: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("ckptfloor: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("ckptfloor: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("ckptfloor: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("ckptfloor: rename: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
