package Block

import (
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// ── convertToPbTransaction ────────────────────────────────────────────────────
//
// The v1 commonv1.Transaction is field-identical to the legacy wire type
// (verified field-by-field, docs/MRE-V1-MIGRATION-TRACKER.md §2). These tests
// pin the converter's behavior so the parity claim stays true under change:
// every field mapped, nil-safety honored, and the legacy GasPrice→MaxFee
// fallback preserved.

func addr(hex string) *common.Address {
	a := common.HexToAddress(hex)
	return &a
}

func fullTx() *config.Transaction {
	return &config.Transaction{
		Hash:           common.HexToHash("0xabc123"),
		From:           addr("0x1111111111111111111111111111111111111111"),
		To:             addr("0x2222222222222222222222222222222222222222"),
		Value:          big.NewInt(1_000_000_000_000_000_000), // 1 token
		Type:           2,
		Timestamp:      1752570000,
		ChainID:        big.NewInt(7000700),
		Nonce:          42,
		GasLimit:       21000,
		GasPrice:       big.NewInt(35_000_000_000),
		MaxFee:         big.NewInt(70_000_000_000),
		MaxPriorityFee: big.NewInt(2_000_000_000),
		Data:           []byte{0xde, 0xad},
		V:              big.NewInt(1),
		R:              big.NewInt(0x1234),
		S:              big.NewInt(0x5678),
		AccessList: config.AccessList{
			{
				Address:     common.HexToAddress("0x3333333333333333333333333333333333333333"),
				StorageKeys: []common.Hash{common.HexToHash("0x01"), common.HexToHash("0x02")},
			},
		},
	}
}

// TestConvertToPbTransaction_AllFields pins the full 17-field mapping for a
// type-2 (EIP-1559) transaction.
func TestConvertToPbTransaction_AllFields(t *testing.T) {
	tx := fullTx()
	const hash = "0xabc123"

	pb := convertToPbTransaction(tx, hash)

	checks := []struct {
		name, got, want string
	}{
		{"Hash", pb.Hash, hash},
		{"From", pb.From, tx.From.Hex()},
		{"To", pb.To, tx.To.Hex()},
		{"Value", pb.Value, "1000000000000000000"},
		{"ChainId", pb.ChainId, "7000700"},
		{"GasLimit", pb.GasLimit, "21000"},
		{"GasPrice", pb.GasPrice, "35000000000"},
		{"MaxFee", pb.MaxFee, "70000000000"},
		{"MaxPriorityFee", pb.MaxPriorityFee, "2000000000"},
		{"V", pb.V, "0x1"},
		{"R", pb.R, "0x1234"},
		{"S", pb.S, "0x5678"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	if pb.Type != 2 {
		t.Errorf("Type = %d, want 2", pb.Type)
	}
	if pb.Nonce != 42 {
		t.Errorf("Nonce = %d, want 42", pb.Nonce)
	}
	if pb.Timestamp != 1752570000 {
		t.Errorf("Timestamp = %d, want 1752570000", pb.Timestamp)
	}
	if string(pb.Data) != string([]byte{0xde, 0xad}) {
		t.Errorf("Data = %x, want dead", pb.Data)
	}

	if len(pb.AccessList) != 1 {
		t.Fatalf("AccessList length = %d, want 1", len(pb.AccessList))
	}
	if got := pb.AccessList[0].Address; got != tx.AccessList[0].Address.Hex() {
		t.Errorf("AccessList address = %q, want %q", got, tx.AccessList[0].Address.Hex())
	}
	if got := len(pb.AccessList[0].StorageKeys); got != 2 {
		t.Errorf("StorageKeys length = %d, want 2", got)
	}
}

// TestConvertToPbTransaction_LegacyGasPriceFallback pins the type-0 rule:
// when MaxFee is unset, GasPrice becomes the effective MaxFee so downstream
// fee handling always has a ceiling.
func TestConvertToPbTransaction_LegacyGasPriceFallback(t *testing.T) {
	tx := fullTx()
	tx.Type = 0
	tx.MaxFee = nil // unset → converter renders "0" then falls back

	pb := convertToPbTransaction(tx, "0xh")

	if pb.MaxFee != tx.GasPrice.String() {
		t.Errorf("legacy fallback: MaxFee = %q, want GasPrice %q", pb.MaxFee, tx.GasPrice.String())
	}
}

// TestConvertToPbTransaction_LegacyFallbackNotAppliedWhenMaxFeeSet guards the
// inverse: an explicitly set MaxFee on a legacy tx must not be overwritten.
func TestConvertToPbTransaction_LegacyFallbackNotAppliedWhenMaxFeeSet(t *testing.T) {
	tx := fullTx()
	tx.Type = 0

	pb := convertToPbTransaction(tx, "0xh")

	if pb.MaxFee != "70000000000" {
		t.Errorf("MaxFee = %q, want explicit 70000000000 (fallback must not fire)", pb.MaxFee)
	}
}

// TestConvertToPbTransaction_NilSafety pins nil handling: nil big.Ints render
// as "0", nil/zero signatures as "0x0", nil To/Data as empty — never panics.
func TestConvertToPbTransaction_NilSafety(t *testing.T) {
	tx := &config.Transaction{
		From: addr("0x1111111111111111111111111111111111111111"),
		// everything else nil / zero
	}

	pb := convertToPbTransaction(tx, "0xh")

	if pb.Value != "0" || pb.ChainId != "0" || pb.GasPrice != "0" || pb.MaxPriorityFee != "0" {
		t.Errorf("nil big.Ints must render \"0\": value=%q chainid=%q gasprice=%q maxprio=%q",
			pb.Value, pb.ChainId, pb.GasPrice, pb.MaxPriorityFee)
	}
	if pb.V != "0x0" || pb.R != "0x0" || pb.S != "0x0" {
		t.Errorf("nil signatures must render \"0x0\": v=%q r=%q s=%q", pb.V, pb.R, pb.S)
	}
	if pb.To != "" {
		t.Errorf("nil To must render empty (contract creation), got %q", pb.To)
	}
	if pb.Data == nil || len(pb.Data) != 0 {
		t.Errorf("nil Data must render empty non-nil slice, got %v", pb.Data)
	}
	if pb.AccessList != nil {
		t.Errorf("empty access list must render nil, got %v", pb.AccessList)
	}
}
