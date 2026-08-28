package backend

// Write-side tests for the AVC consensus-field persistence pass (2026-08-26).
//
// Slot/Period already had this coverage (block_persist_slot_test.go); the other
// six fields did not, and were silently dropped by toBlockRecord. The read-side
// half is tested in DB_OPs/thebe_conversions_consensus_fields_test.go — the two
// halves live in different packages because toBlockRecord and
// blockRecordToZKBlock are each unexported in their own.

import (
	"encoding/json"
	"testing"

	"gossipnode/config"
)

// fullConsensusBlock builds a block carrying a non-zero value for every AVC
// consensus field, so a dropped field shows up as a concrete mismatch rather
// than passing by coincidence against a zero default.
func fullConsensusBlock() *config.ZKBlock {
	return &config.ZKBlock{
		BlockNumber: 100,
		Slot:        5003,
		Period:      2,
		RandaoReveals: []config.Reveal{
			{ProposerID: "12D3KooWAlice", Secret: []byte("alice-reveal-secret-32-bytes----")},
			{ProposerID: "12D3KooWBob", Secret: []byte("bob-reveal-secret-32-bytes------")},
		},
		VdfProof:            []byte("vdf-proof-bytes"),
		SeedEpoch:           9,
		VotingSnapshotEpoch: 31,
		PrevAggCert: []config.CertSigner{
			{PeerID: "12D3KooWAlice", PubKey: "aabb", Signature: "1122"},
			{PeerID: "12D3KooWBob", PubKey: "ccdd", Signature: "3344"},
		},
		CommitteeSnapshotHash: []byte("committee-snapshot-hash-32-bytes"),
	}
}

func TestToBlockRecord_PersistsEveryConsensusField(t *testing.T) {
	rec := toBlockRecord(fullConsensusBlock())

	for _, key := range []string{
		"slot", "period",
		"randao_reveals", "vdf_proof", "seed_epoch",
		"voting_snapshot_epoch", "prev_agg_cert", "committee_snapshot_hash",
	} {
		if _, ok := rec.ExtraData[key]; !ok {
			t.Errorf("ExtraData is missing %q — the field would be dropped on write", key)
		}
	}

	if got := rec.ExtraData["seed_epoch"]; got != uint64(9) {
		t.Errorf("seed_epoch = %v, want 9", got)
	}
	if got := rec.ExtraData["voting_snapshot_epoch"]; got != uint64(31) {
		t.Errorf("voting_snapshot_epoch = %v, want 31", got)
	}
}

// The write is unconditional: a nil slice and a zero epoch must still produce a
// key. Without this, a reader cannot distinguish "this block genuinely carried
// no reveals" from "this record predates the fix" — and for the fallback fold
// that is the difference between a real gap and an artifact.
func TestToBlockRecord_WritesKeysEvenWhenEmpty(t *testing.T) {
	rec := toBlockRecord(&config.ZKBlock{BlockNumber: 1}) // every consensus field zero/nil

	for _, key := range []string{
		"randao_reveals", "vdf_proof", "seed_epoch",
		"voting_snapshot_epoch", "prev_agg_cert", "committee_snapshot_hash",
	} {
		if _, ok := rec.ExtraData[key]; !ok {
			t.Errorf("ExtraData omits %q for an empty value — 'absent' and 'empty' must stay distinguishable", key)
		}
	}
}

// ExtraData is stored as JSONB, so whatever toBlockRecord puts in the map must
// survive json.Marshal. A type that cannot be marshalled would fail at the
// database boundary, far from here and with a much worse error.
func TestToBlockRecord_ExtraDataIsJSONMarshalable(t *testing.T) {
	rec := toBlockRecord(fullConsensusBlock())
	if _, err := json.Marshal(rec.ExtraData); err != nil {
		t.Fatalf("ExtraData does not marshal to JSON: %v", err)
	}
}

// toBlockRecordWithZK layers the ZK keys on top; it must not clobber the
// consensus fields toBlockRecord just wrote.
func TestToBlockRecordWithZK_PreservesConsensusFields(t *testing.T) {
	b := fullConsensusBlock()
	b.ProofHash = "0xproof"
	b.Status = "verified"

	rec := toBlockRecordWithZK(b)

	if _, ok := rec.ExtraData["prev_agg_cert"]; !ok {
		t.Error("toBlockRecordWithZK dropped prev_agg_cert")
	}
	if _, ok := rec.ExtraData["vdf_proof"]; !ok {
		t.Error("toBlockRecordWithZK dropped vdf_proof")
	}
	if rec.ExtraData["proof_hash"] != "0xproof" {
		t.Errorf("proof_hash = %v, want 0xproof", rec.ExtraData["proof_hash"])
	}
}
