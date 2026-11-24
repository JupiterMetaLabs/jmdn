package config

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func addr(b byte) common.Address { var a common.Address; a[19] = b; return a }

func bal(m map[common.Address]*big.Int, a common.Address) int64 {
	if v, ok := m[a]; ok {
		return v.Int64()
	}
	return 0
}

// A payable call: sender sends 100 value to a contract; then pays a 10 gas fee.
// zkvm gets floor(10/2)=5, coinbase gets 5. Balances must reconcile exactly and
// conserve native coin.
func TestFoldContractExecution_PayableCallWithGas(t *testing.T) {
	sender, contract := addr(1), addr(2)
	zkvm, coinbase := addr(3), addr(4)

	pre := map[common.Address]*big.Int{
		sender: bi(1000), contract: bi(0), zkvm: bi(0), coinbase: bi(0),
	}
	// EVM moved 100 from sender to contract (gas price 0, so no gas here).
	evmAbs := map[common.Address]*big.Int{
		sender: bi(900), contract: bi(100),
	}

	final, err := FoldContractExecution(pre, evmAbs, sender, zkvm, coinbase, bi(10), nil)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if got := bal(final, sender); got != 890 { // 1000 - 100 value - 10 gas
		t.Fatalf("sender=%d, want 890", got)
	}
	if got := bal(final, contract); got != 100 {
		t.Fatalf("contract=%d, want 100", got)
	}
	if got := bal(final, zkvm); got != 5 {
		t.Fatalf("zkvm=%d, want 5 (floor(10/2))", got)
	}
	if got := bal(final, coinbase); got != 5 {
		t.Fatalf("coinbase=%d, want 5", got)
	}
	// Total conserved: 890+100+5+5 = 1000 = pre total.
	if got := bal(final, sender) + bal(final, contract) + bal(final, zkvm) + bal(final, coinbase); got != 1000 {
		t.Fatalf("total=%d, want 1000 (conserved)", got)
	}
}

// A non-payable contract (logic/storage only): no value moves, only gas is charged.
func TestFoldContractExecution_NoValueOnlyGas(t *testing.T) {
	sender, zkvm, coinbase := addr(1), addr(3), addr(4)
	pre := map[common.Address]*big.Int{sender: bi(1000)}
	// EVM touched nothing balance-wise (storage-only) -> empty absolute set.
	final, err := FoldContractExecution(pre, map[common.Address]*big.Int{}, sender, zkvm, coinbase, bi(20), nil)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if bal(final, sender) != 980 || bal(final, zkvm) != 10 || bal(final, coinbase) != 10 {
		t.Fatalf("got sender=%d zkvm=%d coinbase=%d, want 980/10/10", bal(final, sender), bal(final, zkvm), bal(final, coinbase))
	}
}

// Native coin NOT conserved (EVM "minted" 50) must be rejected fail-closed.
func TestFoldContractExecution_RejectsMintedValue(t *testing.T) {
	sender, contract, zkvm, coinbase := addr(1), addr(2), addr(3), addr(4)
	pre := map[common.Address]*big.Int{sender: bi(1000), contract: bi(0)}
	// sender -100 but contract +150 → +50 created out of nowhere.
	evmAbs := map[common.Address]*big.Int{sender: bi(900), contract: bi(150)}
	if _, err := FoldContractExecution(pre, evmAbs, sender, zkvm, coinbase, bi(10), nil); err == nil {
		t.Fatal("must reject non-conserved value (minted coin)")
	}
}

// Insolvent sender (value + gas exceeds balance) must be rejected fail-closed.
func TestFoldContractExecution_RejectsNegative(t *testing.T) {
	sender, contract, zkvm, coinbase := addr(1), addr(2), addr(3), addr(4)
	pre := map[common.Address]*big.Int{sender: bi(100), contract: bi(0)}
	// EVM moved 100 out (sender→contract), leaving 0; gas 10 then underflows.
	evmAbs := map[common.Address]*big.Int{sender: bi(0), contract: bi(100)}
	if _, err := FoldContractExecution(pre, evmAbs, sender, zkvm, coinbase, bi(10), nil); err == nil {
		t.Fatal("must reject an ending negative balance (insufficient for value+gas)")
	}
}

// Weighted fee recipients: coinbase share split, conservation holds to the wei.
func TestFoldContractExecution_WeightedRecipients(t *testing.T) {
	sender, zkvm, coinbase := addr(1), addr(3), addr(4)
	r1, r2 := addr(5), addr(6)
	pre := map[common.Address]*big.Int{sender: bi(1000), zkvm: bi(0), r1: bi(0), r2: bi(0)}
	recips := []FeeRecipient{{Addr: r1, Weight: 1}, {Addr: r2, Weight: 3}}

	// gasFee 100 → zkvm floor=50, coinbaseShare=50 split 1:3 → r1=12(+2 rem)?,
	// r2=37. SplitFee assigns the remainder to the canonical-first recipient.
	final, err := FoldContractExecution(pre, map[common.Address]*big.Int{}, sender, zkvm, coinbase, bi(100), recips)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	// Cross-check against SplitFee directly (single source of truth).
	zkvmShare, credits := SplitFee(bi(100), coinbase, recips)
	if bal(final, zkvm) != zkvmShare.Int64() {
		t.Fatalf("zkvm=%d, want %d", bal(final, zkvm), zkvmShare.Int64())
	}
	creditSum := int64(0)
	for _, c := range credits {
		if bal(final, c.Addr) != c.Amount.Int64() {
			t.Fatalf("recipient %s=%d, want %d", c.Addr.Hex(), bal(final, c.Addr), c.Amount.Int64())
		}
		creditSum += c.Amount.Int64()
	}
	if bal(final, sender) != 900 {
		t.Fatalf("sender=%d, want 900", bal(final, sender))
	}
	// Conservation: sender lost exactly gasFee = zkvmShare + Σcredits.
	if zkvmShare.Int64()+creditSum != 100 {
		t.Fatalf("fee split does not sum to gasFee: %d", zkvmShare.Int64()+creditSum)
	}
}
