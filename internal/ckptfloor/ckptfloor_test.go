package ckptfloor

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingIsNoFloor(t *testing.T) {
	_, ok, err := Load(filepath.Join(t.TempDir(), "floor.json"))
	if err != nil || ok {
		t.Fatalf("missing file: ok=%v err=%v, want false,nil", ok, err)
	}
}

func TestRaiseOnlyMovesUp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "floor.json")
	head := bytes.Repeat([]byte{0xab}, 32)
	if err := Raise(p, 7, 40, head); err != nil {
		t.Fatal(err)
	}
	if err := Raise(p, 7, 20, bytes.Repeat([]byte{0x01}, 32)); err != nil {
		t.Fatal(err)
	}
	f, ok, err := Load(p)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if f.Seq != 40 || !bytes.Equal(f.HeadBytes(), head) || f.ChainID != 7 {
		t.Fatalf("floor lowered or wrong: %+v", f)
	}
	if err := Raise(p, 7, 60, head); err != nil {
		t.Fatal(err)
	}
	if f, _, _ = Load(p); f.Seq != 60 {
		t.Fatalf("floor not raised: %+v", f)
	}
}

// A corrupted floor file must not read as "no floor" — that would switch the
// rollback check off.
func TestCorruptFileIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "floor.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(p); err == nil {
		t.Fatal("expected an error for a corrupt floor file")
	}
}

func TestEmptyPathDisables(t *testing.T) {
	if err := Raise("", 1, 1, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Load(""); ok || err != nil {
		t.Fatalf("empty path: ok=%v err=%v", ok, err)
	}
}
