package l1finality

// S3 (review) regression tests for L1 finality monotonicity. These cover the
// pure decision function (isL1Regression) and the payload validators, which need
// no live DB. The DB-touching apply paths (ApplyCommit/ApplyRange) are exercised
// by the 2-node gate; see docs.

import "testing"

func TestIsL1Regression(t *testing.T) {
	cases := []struct {
		name         string
		existL1      uint64
		newL1        uint64
		wantRejected bool
	}{
		{"unrecorded block accepts any anchor", 0, 100, false},
		{"equal height is idempotent replay", 100, 100, false},
		{"higher height (l1 reorg re-commit) accepted", 100, 101, false},
		{"strictly lower height is a regression", 100, 99, true},
		{"downgrade to zero is a regression", 100, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isL1Regression(c.existL1, c.newL1); got != c.wantRejected {
				t.Fatalf("isL1Regression(%d,%d)=%v want %v", c.existL1, c.newL1, got, c.wantRejected)
			}
		})
	}
}

func TestRangePayloadValidate(t *testing.T) {
	good := RangePayload{StartBlock: 10, EndBlock: 12, L1TxHash: "0xabc", L1BlockNumber: 5}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid range rejected: %v", err)
	}
	tooBig := RangePayload{StartBlock: 1, EndBlock: MaxRangeSpan + 1, L1TxHash: "0xabc"}
	if err := tooBig.Validate(); err == nil {
		t.Fatalf("oversized range must be rejected (span %d > %d)", MaxRangeSpan+1, MaxRangeSpan)
	}
	exactCap := RangePayload{StartBlock: 1, EndBlock: MaxRangeSpan, L1TxHash: "0xabc"}
	if err := exactCap.Validate(); err != nil {
		t.Fatalf("span exactly MaxRangeSpan (%d) must be allowed: %v", MaxRangeSpan, err)
	}
	if err := (RangePayload{StartBlock: 5, EndBlock: 4, L1TxHash: "0xabc"}).Validate(); err == nil {
		t.Fatalf("end < start must be rejected")
	}
	if err := (RangePayload{StartBlock: 5, EndBlock: 6}).Validate(); err == nil {
		t.Fatalf("missing l1_tx_hash must be rejected")
	}
}

func TestCommitPayloadValidate(t *testing.T) {
	if err := (CommitPayload{BlockNumber: 7, L1TxHash: "0xabc"}).Validate(); err != nil {
		t.Fatalf("valid commit rejected: %v", err)
	}
	if err := (CommitPayload{BlockNumber: 0, L1TxHash: "0xabc"}).Validate(); err == nil {
		t.Fatalf("block_number 0 must be rejected")
	}
	if err := (CommitPayload{BlockNumber: 7}).Validate(); err == nil {
		t.Fatalf("missing l1_tx_hash must be rejected")
	}
}
