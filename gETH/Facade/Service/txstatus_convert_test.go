package Service

import (
	"errors"
	"math/big"
	"testing"

	"gossipnode/txstatus"

	"github.com/ethereum/go-ethereum/common"
)

var convChainID = big.NewInt(8000800)

// The single most important property of the conversion: a pending transaction
// carries NO block fields.
//
// marshalTx emits blockHash / blockNumber / transactionIndex as JSON null when
// these are unset, which is exactly the Ethereum pending representation that
// wallets already understand. Populating any of them with a placeholder would
// tell a client the transaction had been mined.
func TestPendingTxToFacadeTx_HasNoBlockFields(t *testing.T) {
	tx := pendingTxToFacadeTx(&txstatus.PendingTx{
		Hash:  "0xabc123",
		From:  "0x1111111111111111111111111111111111111111",
		To:    "0x2222222222222222222222222222222222222222",
		Value: "1000",
		Nonce: 7,
	}, convChainID)

	if tx == nil {
		t.Fatal("conversion returned nil for a valid pending transaction")
	}
	if tx.BlockNumber != nil {
		t.Errorf("BlockNumber = %v, want nil — a pending transaction must not claim a block", *tx.BlockNumber)
	}
	if tx.BlockHash != nil {
		t.Errorf("BlockHash = %x, want nil", tx.BlockHash)
	}
	if tx.TransactionIndex != nil {
		t.Errorf("TransactionIndex = %v, want nil", *tx.TransactionIndex)
	}
}

func TestPendingTxToFacadeTx_MapsFields(t *testing.T) {
	from := "0x1111111111111111111111111111111111111111"
	to := "0x2222222222222222222222222222222222222222"

	tx := pendingTxToFacadeTx(&txstatus.PendingTx{
		Hash:           "0x" + common.HexToHash("0x99").Hex()[2:],
		From:           from,
		To:             to,
		Value:          "1000",
		Type:           2,
		ChainID:        "8000800",
		Nonce:          9,
		GasLimit:       "21000",
		GasPrice:       "35000000000",
		MaxFee:         "40000000000",
		MaxPriorityFee: "2000000000",
		Data:           []byte{0xde, 0xad},
		V:              "1",
		R:              "12345",
		S:              "67890",
	}, convChainID)

	if tx == nil {
		t.Fatal("conversion returned nil")
	}
	if got := common.BytesToAddress(tx.From).Hex(); got != common.HexToAddress(from).Hex() {
		t.Errorf("From = %s, want %s", got, from)
	}
	if got := common.BytesToAddress(tx.To).Hex(); got != common.HexToAddress(to).Hex() {
		t.Errorf("To = %s, want %s", got, to)
	}
	if tx.Nonce != 9 {
		t.Errorf("Nonce = %d, want 9", tx.Nonce)
	}
	if tx.Gas != 21000 {
		t.Errorf("Gas = %d, want 21000", tx.Gas)
	}
	if tx.Type != 2 {
		t.Errorf("Type = %d, want 2", tx.Type)
	}
	if tx.V != 1 {
		t.Errorf("V = %d, want 1", tx.V)
	}
	if got := new(big.Int).SetBytes(tx.Value).String(); got != "1000" {
		t.Errorf("Value = %s, want 1000", got)
	}
	if got := new(big.Int).SetBytes(tx.MaxFeePerGas).String(); got != "40000000000" {
		t.Errorf("MaxFeePerGas = %s, want 40000000000", got)
	}
	if got := new(big.Int).SetBytes(tx.ChainID).String(); got != "8000800" {
		t.Errorf("ChainID = %s, want 8000800", got)
	}
	if string(tx.Input) != "\xde\xad" {
		t.Errorf("Input = %x, want dead", tx.Input)
	}
}

// The mempool carries numbers as strings and does not guarantee the encoding
// across transaction types, so both hex and decimal must parse.
func TestParseNumeric_AcceptsHexAndDecimal(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"0x10", "16", true},
		{"0X10", "16", true},
		{"16", "16", true},
		{"0", "0", true},
		{"", "", false},
		{"   ", "", false},
		{"not-a-number", "", false},
	}
	for _, c := range cases {
		got, ok := parseNumeric(c.in)
		if ok != c.ok {
			t.Errorf("parseNumeric(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got.String() != c.want {
			t.Errorf("parseNumeric(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// An unparseable field must leave a zero value rather than fail the whole
// conversion: a partially-populated pending transaction is more useful to a
// wallet than an error, especially given the mempool's encryption boundary may
// legitimately leave fields empty.
func TestPendingTxToFacadeTx_ToleratesUnparseableFields(t *testing.T) {
	tx := pendingTxToFacadeTx(&txstatus.PendingTx{
		Hash:     "0xabc",
		Value:    "garbage",
		GasLimit: "also-garbage",
		V:        "nope",
	}, convChainID)

	if tx == nil {
		t.Fatal("conversion failed on unparseable numeric fields")
	}
	if len(tx.Value) != 0 {
		t.Errorf("Value = %x, want empty", tx.Value)
	}
	if tx.Gas != 0 {
		t.Errorf("Gas = %d, want 0", tx.Gas)
	}
	if tx.V != 0 {
		t.Errorf("V = %d, want 0", tx.V)
	}
}

// If the mempool does not decrypt the sensitive fields, everything except the
// hash arrives empty. That must still convert — the caller decides what to do
// with a skeleton — but a body with no hash at all is not a transaction.
func TestPendingTxToFacadeTx_HashIsTheOnlyRequirement(t *testing.T) {
	if got := pendingTxToFacadeTx(&txstatus.PendingTx{Hash: "0xabc"}, convChainID); got == nil {
		t.Error("a hash-only pending transaction should still convert")
	}
	if got := pendingTxToFacadeTx(&txstatus.PendingTx{Hash: ""}, convChainID); got != nil {
		t.Error("a body with no hash should not convert")
	}
	if got := pendingTxToFacadeTx(nil, convChainID); got != nil {
		t.Error("nil should convert to nil")
	}
}

// With no chain ID on the transaction, fall back to the node's — otherwise
// ethers.js rejects the response.
func TestPendingTxToFacadeTx_FallsBackToNodeChainID(t *testing.T) {
	tx := pendingTxToFacadeTx(&txstatus.PendingTx{Hash: "0xabc"}, convChainID)
	if tx == nil {
		t.Fatal("conversion returned nil")
	}
	if got := new(big.Int).SetBytes(tx.ChainID).String(); got != convChainID.String() {
		t.Errorf("ChainID = %s, want the node chain ID %s", got, convChainID)
	}
}

func TestPendingTxToFacadeTx_MapsAccessList(t *testing.T) {
	tx := pendingTxToFacadeTx(&txstatus.PendingTx{
		Hash: "0xabc",
		AccessList: []txstatus.AccessTuple{{
			Address:     "0x3333333333333333333333333333333333333333",
			StorageKeys: []string{"0x01", "0x02"},
		}},
	}, convChainID)

	if tx == nil || tx.AccessList == nil {
		t.Fatal("access list was dropped")
	}
	al := *tx.AccessList
	if len(al) != 1 {
		t.Fatalf("access list entries = %d, want 1", len(al))
	}
	if al[0].Address != common.HexToAddress("0x3333333333333333333333333333333333333333") {
		t.Errorf("access list address = %s", al[0].Address.Hex())
	}
	if len(al[0].StorageKeys) != 2 {
		t.Errorf("storage keys = %d, want 2", len(al[0].StorageKeys))
	}
}

// isNotFoundErr decides whether a chain-store error means "absent" or "the
// database could not answer". Matching too broadly would turn a real failure
// into a confident "not mined", so the recognised set is deliberately narrow.
func TestIsNotFoundErr(t *testing.T) {
	notFound := []error{
		errors.New("transaction not found"),
		errors.New("Transaction Not Found"),
		errors.New("sql: no rows in result set"),
		errors.New("relation does not exist"),
	}
	for _, err := range notFound {
		if !isNotFoundErr(err) {
			t.Errorf("isNotFoundErr(%q) = false, want true", err)
		}
	}

	realFailures := []error{
		nil,
		errors.New("connection refused"),
		errors.New("context deadline exceeded"),
		errors.New("permission denied for table transactions"),
		errors.New("TLS handshake failure"),
	}
	for _, err := range realFailures {
		if isNotFoundErr(err) {
			t.Errorf("isNotFoundErr(%v) = true, want false — a real failure would be read as 'not mined'", err)
		}
	}
}

// The feature must be inert until both switches are on: an installed resolver
// alone does not change eth_getTransactionByHash.
func TestPendingTxByHashEnabled_RequiresBothSwitches(t *testing.T) {
	t.Cleanup(func() {
		SetTxStatusResolver(nil)
		SetPendingTxByHashEnabled(false)
	})

	SetTxStatusResolver(nil)
	SetPendingTxByHashEnabled(true)
	if pendingTxByHashEnabled() {
		t.Error("enabled with no resolver installed")
	}

	SetTxStatusResolver(txstatus.NewResolver(txstatus.Deps{Chain: nil}))
	SetPendingTxByHashEnabled(false)
	if pendingTxByHashEnabled() {
		t.Error("enabled with the pending-tx switch off")
	}

	SetPendingTxByHashEnabled(true)
	if !pendingTxByHashEnabled() {
		t.Error("disabled with both switches on")
	}
}
