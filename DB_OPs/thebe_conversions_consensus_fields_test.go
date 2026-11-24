package DB_OPs

// Read-side tests for the AVC consensus-field persistence pass (2026-08-26),
// the counterpart to DB_OPs/backend/block_persist_consensus_fields_test.go.
//
// Every test here drives the value through a REAL json.Marshal/Unmarshal cycle
// before decoding, because that is the only shape production ever sees:
// thebegateway/reader.go's scanBlock and the cache decorator in
// DB_OPs/store/cache/block.go both populate BlockRecord.ExtraData by
// unmarshalling JSONB into a map[string]any. A test that hand-built the map
// with the "right" Go types would pass even with the decode entirely broken —
// []config.Reveal arrives as []any of map[string]any, and []byte arrives as a
// base64 string, never as themselves.

import (
	"encoding/json"
	"strings"
	"testing"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
)

// throughJSON round-trips an ExtraData map exactly as the SQL scan does.
func throughJSON(t *testing.T, written map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(written)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return out
}

func TestBlockRecordToZKBlock_RecoversEveryConsensusField(t *testing.T) {
	reveals := []config.Reveal{
		{ProposerID: "12D3KooWAlice", Secret: []byte("alice-secret")},
		{ProposerID: "12D3KooWBob", Secret: []byte("bob-secret")},
	}
	cert := []config.CertSigner{
		{PeerID: "12D3KooWAlice", PubKey: "aabb", Signature: "1122"},
		{PeerID: "12D3KooWBob", PubKey: "ccdd", Signature: "3344"},
	}

	rec := &thebegateway.BlockRecord{
		BlockNumber: 100,
		ExtraData: throughJSON(t, map[string]any{
			"slot":                    uint64(5003),
			"period":                  uint64(2),
			"randao_reveals":          reveals,
			"vdf_proof":               []byte("vdf-proof-bytes"),
			"seed_epoch":              uint64(9),
			"voting_snapshot_epoch":   uint64(31),
			"prev_agg_cert":           cert,
			"committee_snapshot_hash": []byte("committee-snapshot-hash"),
		}),
	}

	blk, err := blockRecordToZKBlock(rec)
	if err != nil {
		t.Fatalf("blockRecordToZKBlock: %v", err)
	}

	if blk.Slot != 5003 || blk.Period != 2 {
		t.Errorf("Slot/Period = %d/%d, want 5003/2", blk.Slot, blk.Period)
	}
	if blk.SeedEpoch != 9 {
		t.Errorf("SeedEpoch = %d, want 9", blk.SeedEpoch)
	}
	if blk.VotingSnapshotEpoch != 31 {
		t.Errorf("VotingSnapshotEpoch = %d, want 31", blk.VotingSnapshotEpoch)
	}
	if string(blk.VdfProof) != "vdf-proof-bytes" {
		t.Errorf("VdfProof = %q, want %q", blk.VdfProof, "vdf-proof-bytes")
	}
	if string(blk.CommitteeSnapshotHash) != "committee-snapshot-hash" {
		t.Errorf("CommitteeSnapshotHash = %q, want %q", blk.CommitteeSnapshotHash, "committee-snapshot-hash")
	}

	if len(blk.RandaoReveals) != 2 {
		t.Fatalf("got %d reveals, want 2", len(blk.RandaoReveals))
	}
	if blk.RandaoReveals[0].ProposerID != "12D3KooWAlice" ||
		string(blk.RandaoReveals[0].Secret) != "alice-secret" {
		t.Errorf("reveal[0] = %+v, want Alice/alice-secret", blk.RandaoReveals[0])
	}

	if len(blk.PrevAggCert) != 2 {
		t.Fatalf("got %d cert signers, want 2", len(blk.PrevAggCert))
	}
	if blk.PrevAggCert[0] != cert[0] || blk.PrevAggCert[1] != cert[1] {
		t.Errorf("PrevAggCert = %+v, want %+v", blk.PrevAggCert, cert)
	}
}

// Certificate order is load-bearing, not cosmetic: RecordCommitCertificate
// hash-covers a certificate in array order, so a decode that reordered signers
// would change the derived aggregate and break verification. Pinned with a
// deliberately non-alphabetical order, which a map-keyed decode would silently
// sort.
func TestBlockRecordToZKBlock_PreservesCertSignerOrder(t *testing.T) {
	cert := []config.CertSigner{
		{PeerID: "zeta", PubKey: "01", Signature: "aa"},
		{PeerID: "alpha", PubKey: "02", Signature: "bb"},
		{PeerID: "mike", PubKey: "03", Signature: "cc"},
	}
	rec := &thebegateway.BlockRecord{
		ExtraData: throughJSON(t, map[string]any{"prev_agg_cert": cert}),
	}

	blk, err := blockRecordToZKBlock(rec)
	if err != nil {
		t.Fatalf("blockRecordToZKBlock: %v", err)
	}
	for i := range cert {
		if blk.PrevAggCert[i] != cert[i] {
			t.Fatalf("signer %d = %+v, want %+v — decode must not reorder", i, blk.PrevAggCert[i], cert[i])
		}
	}
}

// Every record written before this fix has none of these keys. That must decode
// to zero values with no error, or the change would break reads of all existing
// history.
func TestBlockRecordToZKBlock_MissingConsensusKeysAreNotAnError(t *testing.T) {
	rec := &thebegateway.BlockRecord{
		BlockNumber: 7,
		ExtraData:   map[string]any{"raw": "legacy"},
	}

	blk, err := blockRecordToZKBlock(rec)
	if err != nil {
		t.Fatalf("a pre-fix record must still decode cleanly, got: %v", err)
	}
	if blk.RandaoReveals != nil || blk.PrevAggCert != nil ||
		blk.VdfProof != nil || blk.CommitteeSnapshotHash != nil {
		t.Error("absent keys should leave the fields nil")
	}
	if blk.SeedEpoch != 0 || blk.VotingSnapshotEpoch != 0 {
		t.Error("absent epoch keys should leave the fields zero")
	}
}

// An empty-but-present value is distinct from an absent one and must decode
// cleanly — this is the case toBlockRecord's unconditional write produces for a
// block that genuinely carried no reveals.
func TestBlockRecordToZKBlock_EmptyValuesDecodeCleanly(t *testing.T) {
	rec := &thebegateway.BlockRecord{
		ExtraData: throughJSON(t, map[string]any{
			"randao_reveals":          []config.Reveal(nil),
			"prev_agg_cert":           []config.CertSigner(nil),
			"vdf_proof":               []byte(nil),
			"committee_snapshot_hash": []byte(nil),
			"seed_epoch":              uint64(0),
		}),
	}

	blk, err := blockRecordToZKBlock(rec)
	if err != nil {
		t.Fatalf("empty values must decode cleanly, got: %v", err)
	}
	if len(blk.RandaoReveals) != 0 || len(blk.PrevAggCert) != 0 || len(blk.VdfProof) != 0 {
		t.Error("empty values should decode to empty, not to junk")
	}
}

// The fail-closed property, per field. A corrupt value must surface as an
// error, never as a silent nil: nil is indistinguishable from "this block
// legitimately carried no signers", which would make the fallback fold treat
// corruption as a real gap and produce a wrong seed instead of refusing.
func TestBlockRecordToZKBlock_MalformedConsensusFieldsFailClosed(t *testing.T) {
	cases := []struct {
		name string
		key  string
		bad  any
	}{
		{"reveals not an array", "randao_reveals", "not-an-array"},
		{"reveals wrong element shape", "randao_reveals", []any{map[string]any{"proposer_id": 12345}}},
		{"cert not an array", "prev_agg_cert", 42.0},
		{"cert wrong element shape", "prev_agg_cert", []any{map[string]any{"peer_id": []any{1, 2}}}},
		{"vdf proof not base64", "vdf_proof", "!!!not-base64!!!"},
		{"vdf proof wrong type", "vdf_proof", 3.14},
		{"snapshot hash wrong type", "committee_snapshot_hash", map[string]any{"nope": true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &thebegateway.BlockRecord{
				BlockNumber: 55,
				ExtraData:   map[string]any{tc.key: tc.bad},
			}
			blk, err := blockRecordToZKBlock(rec)
			if err == nil {
				t.Fatalf("a malformed %s decoded without error (got %+v) — corruption must not look like absence", tc.key, blk)
			}
			// The error has to name the block and the field, or an operator
			// reading it cannot tell which record to go look at.
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error should name the field %q, got: %v", tc.key, err)
			}
			if !strings.Contains(err.Error(), "55") {
				t.Errorf("error should name the block number, got: %v", err)
			}
		})
	}
}

func TestExtraDataBytes_HandlesEveryObservedShape(t *testing.T) {
	// Post-JSON: base64 string. This is the production shape.
	got, err := extraDataBytes("aGVsbG8=")
	if err != nil || string(got) != "hello" {
		t.Errorf("base64 string decoded to %q (err %v), want %q", got, err, "hello")
	}
	// Pre-JSON: raw bytes, from an in-process writer.
	got, err = extraDataBytes([]byte("hello"))
	if err != nil || string(got) != "hello" {
		t.Errorf("[]byte passed through as %q (err %v), want %q", got, err, "hello")
	}
	// Absent and empty are both legitimately nil, not errors.
	if got, err := extraDataBytes(nil); err != nil || got != nil {
		t.Errorf("nil = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := extraDataBytes(""); err != nil || got != nil {
		t.Errorf(`"" = (%v, %v), want (nil, nil)`, got, err)
	}
	// Anything else is corruption.
	if _, err := extraDataBytes(42.0); err == nil {
		t.Error("a float decoded as bytes without error")
	}
	if _, err := extraDataBytes("!!!not-base64!!!"); err == nil {
		t.Error("invalid base64 decoded without error")
	}
}
