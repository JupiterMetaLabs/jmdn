package messaging

import (
	"fmt"
	"math/big"
	"testing"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// wei returns wholeJMDN * 1e18 as a *big.Int.
func wei(wholeJMDN int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(wholeJMDN), config.WeightScaleWei)
}

const (
	addrA = "0x1111111111111111111111111111111111111111"
	addrB = "0x2222222222222222222222222222222222222222"
	addrC = "0x3333333333333333333333333333333333333333"
)

// TestDeriveFeeRecipients_AggregateByAddress: one address backing two signers
// sums their weights into a single recipient.
func TestDeriveFeeRecipients_AggregateByAddress(t *testing.T) {
	signers := []config.CertSigner{{PeerID: "peer1"}, {PeerID: "peer2"}}
	rewardBy := map[string]string{"peer1": addrA, "peer2": addrA} // same address
	balances := map[common.Address]*big.Int{
		common.HexToAddress(addrA): wei(5), // StakeWeight = Baseline + 5
	}
	balanceOf := func(a common.Address) (*big.Int, error) { return balances[a], nil }

	got, err := DeriveFeeRecipients(signers, rewardBy, balanceOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 aggregated recipient, got %d: %+v", len(got), got)
	}
	want := 2 * (config.BaselineWeight + 5) // two signers, same address, summed
	if got[0].Addr != common.HexToAddress(addrA) || got[0].Weight != want {
		t.Fatalf("aggregate mismatch: got %+v, want addr=%s weight=%d", got[0], addrA, want)
	}
}

// TestDeriveFeeRecipients_OmitUnbound: a signer with no bound address is omitted.
func TestDeriveFeeRecipients_OmitUnbound(t *testing.T) {
	signers := []config.CertSigner{{PeerID: "bound"}, {PeerID: "unbound"}}
	rewardBy := map[string]string{"bound": addrA} // "unbound" absent from the map
	balanceOf := func(a common.Address) (*big.Int, error) { return big.NewInt(0), nil }

	got, err := DeriveFeeRecipients(signers, rewardBy, balanceOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Addr != common.HexToAddress(addrA) {
		t.Fatalf("expected only the bound signer, got %+v", got)
	}
}

// TestDeriveFeeRecipients_NoneBound: with no bound address at all, the result is
// empty so SplitFee falls back to the single coinbase credit.
func TestDeriveFeeRecipients_NoneBound(t *testing.T) {
	signers := []config.CertSigner{{PeerID: "p1"}, {PeerID: "p2"}}
	rewardBy := map[string]string{} // nobody bound
	balanceOf := func(a common.Address) (*big.Int, error) { return big.NewInt(0), nil }

	got, err := DeriveFeeRecipients(signers, rewardBy, balanceOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty recipients, got %+v", got)
	}
}

// TestDeriveFeeRecipients_CanonicalOrderIndependentOfSignerOrder: the output is
// address-sorted regardless of the signer input order.
func TestDeriveFeeRecipients_CanonicalOrderIndependentOfSignerOrder(t *testing.T) {
	balanceOf := func(a common.Address) (*big.Int, error) { return big.NewInt(0), nil }

	forward := []config.CertSigner{{PeerID: "a"}, {PeerID: "b"}, {PeerID: "c"}}
	reverse := []config.CertSigner{{PeerID: "c"}, {PeerID: "b"}, {PeerID: "a"}}
	rewardBy := map[string]string{"a": addrA, "b": addrB, "c": addrC}

	g1, err1 := DeriveFeeRecipients(forward, rewardBy, balanceOf)
	g2, err2 := DeriveFeeRecipients(reverse, rewardBy, balanceOf)
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v %v", err1, err2)
	}
	if !FeeRecipientsEqual(g1, g2) {
		t.Fatalf("order-dependent output: %+v vs %+v", g1, g2)
	}
	// And the slice itself must already be in canonical (address-sorted) order.
	if !(g1[0].Addr == common.HexToAddress(addrA) &&
		g1[1].Addr == common.HexToAddress(addrB) &&
		g1[2].Addr == common.HexToAddress(addrC)) {
		t.Fatalf("output not address-sorted: %+v", g1)
	}
}

// TestDeriveFeeRecipients_FailClosedOnBalanceError: a balance-read error aborts
// derivation (never treated as zero).
func TestDeriveFeeRecipients_FailClosedOnBalanceError(t *testing.T) {
	signers := []config.CertSigner{{PeerID: "p1"}}
	rewardBy := map[string]string{"p1": addrA}
	balanceOf := func(a common.Address) (*big.Int, error) {
		return nil, fmt.Errorf("transient DB failure")
	}

	if _, err := DeriveFeeRecipients(signers, rewardBy, balanceOf); err == nil {
		t.Fatalf("expected fail-closed error on balance read failure, got nil")
	}
}

// TestDeriveFeeRecipients_ZeroBalanceBaseline: a zero-balance address still earns
// exactly BaselineWeight.
func TestDeriveFeeRecipients_ZeroBalanceBaseline(t *testing.T) {
	signers := []config.CertSigner{{PeerID: "p1"}}
	rewardBy := map[string]string{"p1": addrA}
	balanceOf := func(a common.Address) (*big.Int, error) { return big.NewInt(0), nil }

	got, err := DeriveFeeRecipients(signers, rewardBy, balanceOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Weight != config.BaselineWeight {
		t.Fatalf("zero-balance should earn BaselineWeight (%d), got %+v", config.BaselineWeight, got)
	}
}

// TestDeriveFeeRecipients_InvalidBoundAddress: a malformed bound address fails
// closed rather than being skipped.
func TestDeriveFeeRecipients_InvalidBoundAddress(t *testing.T) {
	signers := []config.CertSigner{{PeerID: "p1"}}
	rewardBy := map[string]string{"p1": "0xnothex"}
	balanceOf := func(a common.Address) (*big.Int, error) { return big.NewInt(0), nil }

	if _, err := DeriveFeeRecipients(signers, rewardBy, balanceOf); err == nil {
		t.Fatalf("expected fail-closed error on invalid bound address, got nil")
	}
}

// TestFeeRecipientsEqual: canonical equality is order-independent and
// weight-sensitive.
func TestFeeRecipientsEqual(t *testing.T) {
	a := []config.FeeRecipient{
		{Addr: common.HexToAddress(addrA), Weight: 3},
		{Addr: common.HexToAddress(addrB), Weight: 7},
	}
	bReordered := []config.FeeRecipient{
		{Addr: common.HexToAddress(addrB), Weight: 7},
		{Addr: common.HexToAddress(addrA), Weight: 3},
	}
	if !FeeRecipientsEqual(a, bReordered) {
		t.Fatalf("expected equal (order-independent)")
	}
	bWrongWeight := []config.FeeRecipient{
		{Addr: common.HexToAddress(addrA), Weight: 3},
		{Addr: common.HexToAddress(addrB), Weight: 8},
	}
	if FeeRecipientsEqual(a, bWrongWeight) {
		t.Fatalf("expected unequal on weight difference")
	}
	if FeeRecipientsEqual(a, a[:1]) {
		t.Fatalf("expected unequal on length difference")
	}
}
