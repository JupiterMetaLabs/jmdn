package thebeprofile_test

// SQL-backed regression test for the storage-layer Auspex proof-drop.
//
// zk_proofs.proof_hash is NOT NULL UNIQUE. Auspex blocks carried a StarkProof but an
// empty proof_hash, and the projection insert was an untargeted ON CONFLICT DO NOTHING,
// so the first such block took '' and every later one silently lost its row. This test
// drives the REAL projection (Profile.Apply → applyZKProof → sqlInsertZKProof) against
// SQLite with the table's real key constraints. It fails on 271f9d8 (second row missing,
// collision swallowed) and passes once proof_hash is canonicalized and the conflict
// target is explicit.

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"
	"github.com/ethereum/go-ethereum/crypto"
	_ "github.com/mattn/go-sqlite3"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/DB_OPs/thebeprofile"
)

// The key constraints that matter, mirrored from migrations/000001_init_schema.up.sql
// (created_at/FK omitted: NOW() and the blocks FK are Postgres/fixture concerns, not
// part of the collision behavior under test).
const zkProofsDDL = `
CREATE TABLE zk_proofs (
    block_number BIGINT   PRIMARY KEY,
    proof_hash   CHAR(66) NOT NULL UNIQUE,
    stark_proof  BYTEA    NOT NULL,
    commitment   BYTEA
)`

func zkRecord(t *testing.T, r thebegateway.ZKProofRecord) *core.CanonicalRecord {
	t.Helper()
	v, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return &core.CanonicalRecord{Namespace: "zk", Value: v}
}

func TestApplyZKProof_AuspexEmptyProofHash_NoSilentDrop(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // one connection => one :memory: database
	if _, err := db.Exec(zkProofsDDL); err != nil {
		t.Fatalf("ddl: %v", err)
	}

	p := thebeprofile.NewJMDNProfile()
	ctx := context.Background()
	apply := func(seq uint64, r thebegateway.ZKProofRecord) error {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Apply(ctx, seq, zkRecord(t, r), tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}

	starkA := []byte("auspex-envelope-block-900")
	starkB := []byte("auspex-envelope-block-901")

	// Two Auspex blocks, both with an EMPTY proof_hash (exactly what the orchestrator
	// sent before 01e6341, and what the canonical KV log still holds for old blocks).
	if err := apply(1, thebegateway.ZKProofRecord{BlockNumber: 900, ProofHash: "", StarkProof: starkA}); err != nil {
		t.Fatalf("block 900: %v", err)
	}
	if err := apply(2, thebegateway.ZKProofRecord{BlockNumber: 901, ProofHash: "", StarkProof: starkB}); err != nil {
		t.Fatalf("block 901: %v", err)
	}

	cases := []struct {
		block uint64
		stark []byte
	}{{900, starkA}, {901, starkB}}

	// First: BOTH rows must exist. (On the pre-fix code, 901 collides on proof_hash=''
	// and the untargeted DO NOTHING silently drops it.)
	for _, c := range cases {
		var one int
		err := db.QueryRow(`SELECT 1 FROM zk_proofs WHERE block_number = ?`, c.block).Scan(&one)
		if err == sql.ErrNoRows {
			t.Fatalf("block %d has NO zk_proofs row — proof silently dropped", c.block)
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	// Then: each carries its own proof and its canonical hash.
	for _, c := range cases {
		var hash string
		var stark []byte
		if err := db.QueryRow(`SELECT proof_hash, stark_proof FROM zk_proofs WHERE block_number = ?`, c.block).Scan(&hash, &stark); err != nil {
			t.Fatal(err)
		}
		if want := crypto.Keccak256Hash(c.stark).Hex(); hash != want {
			t.Fatalf("block %d proof_hash = %q, want keccak256(stark) %s", c.block, hash, want)
		}
		if string(stark) != string(c.stark) {
			t.Fatalf("block %d stark_proof not intact", c.block)
		}
	}

	// Replaying the same block (sync re-delivery / reproject) is idempotent, not an error.
	if err := apply(3, thebegateway.ZKProofRecord{BlockNumber: 900, ProofHash: "", StarkProof: starkA}); err != nil {
		t.Fatalf("replay of block 900 must be a no-op, got %v", err)
	}

	// A proof_hash collision between two DIFFERENT blocks must now fail LOUDLY rather
	// than be swallowed (the old untargeted DO NOTHING hid exactly this).
	dup := crypto.Keccak256Hash(starkA).Hex()
	if err := apply(4, thebegateway.ZKProofRecord{BlockNumber: 902, ProofHash: dup, StarkProof: []byte("other")}); err == nil {
		t.Fatal("proof_hash collision across blocks was silently swallowed; want an error")
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM zk_proofs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 rows (900, 901), got %d", n)
	}
}
