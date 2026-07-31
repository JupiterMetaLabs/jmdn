package Security

// Tests for AllowNewReceiverAccounts: sending JMDT to an address that does not
// exist yet.
//
// Requiring the receiver to pre-exist made a first-time recipient impossible —
// every voter checked its own account cache and rejected, which is what took
// block 13756 down 0-of-7 with "receiver account … not found in cache".
//
// The safety line these tests defend: relaxing the RECEIVER check must not
// relax the SENDER check. A sender missing from the cache means this node's
// account state disagrees with a validly signed funded transaction, and that
// must stay a hard reject.

import (
	"context"
	"math/big"
	"os"
	"strings"
	"testing"

	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// withAllowNewReceivers sets the validation flag for one test and restores it.
// The flag is a package var read from the environment at init, so tests set it
// directly rather than through the environment.
func withAllowNewReceivers(t *testing.T, on bool) {
	t.Helper()
	prev := AllowNewReceiverAccounts
	AllowNewReceiverAccounts = on
	t.Cleanup(func() { AllowNewReceiverAccounts = prev })
}

func cacheWith(t *testing.T, funded ...common.Address) *SecurityCache {
	t.Helper()
	c := NewSecurityCache()
	for _, a := range funded {
		c.RegisterAccount(a, &DB_OPs.Account{
			Address: a,
			Balance: "1000000000000000000",
			TxNonce: 0,
		})
	}
	return c
}

func transfer(from, to *common.Address) *config.Transaction {
	return &config.Transaction{
		From:  from,
		To:    to,
		Value: big.NewInt(100000000000000),
		Nonce: 0,
	}
}

func addrOf(hexStr string) *common.Address {
	a := common.HexToAddress(hexStr)
	return &a
}

const (
	senderHex      = "0x4dbE0cbE5D70B2C50a2dE447373B4f8c7C7106A4"
	newReceiverHex = "0x2b9B90970809F32e88ad5fCe11370d10870F217E"
)

// THE FIX: a transfer to an address nobody has seen must be accepted.
func TestNewReceiverAccepted(t *testing.T) {
	withAllowNewReceivers(t, true)

	sender, receiver := addrOf(senderHex), addrOf(newReceiverHex)
	c := cacheWith(t, *sender) // receiver deliberately absent

	ok, err := c.CheckAddressExistWithCache(transfer(sender, receiver), context.Background())
	if err != nil {
		t.Fatalf("transfer to a new address must be accepted, got error: %v", err)
	}
	if !ok {
		t.Fatal("transfer to a new address must be accepted, got ok=false")
	}
}

// Back-compat: with the flag off the previous rule is unchanged, so a mixed
// fleet behaves predictably during rollout.
func TestNewReceiverRejectedWhenFlagOff(t *testing.T) {
	withAllowNewReceivers(t, false)

	sender, receiver := addrOf(senderHex), addrOf(newReceiverHex)
	c := cacheWith(t, *sender)

	ok, err := c.CheckAddressExistWithCache(transfer(sender, receiver), context.Background())
	if ok || err == nil {
		t.Fatal("with the flag off, an absent receiver must still be rejected")
	}
	if !strings.Contains(err.Error(), "receiver account") {
		t.Fatalf("error should name the receiver, got %q", err)
	}
}

// THE SAFETY LINE. Enabling new receivers must not make an unknown SENDER
// acceptable: that means this node's account state is out of sync with a
// validly signed funded transaction, and silently allowing it would let a
// sender with no known balance spend.
func TestUnknownSenderStillRejectedWithFlagOn(t *testing.T) {
	withAllowNewReceivers(t, true)

	sender, receiver := addrOf(senderHex), addrOf(newReceiverHex)
	c := cacheWith(t, *receiver) // receiver known, sender is not

	ok, err := c.CheckAddressExistWithCache(transfer(sender, receiver), context.Background())
	if ok || err == nil {
		t.Fatal("an absent SENDER must be rejected regardless of the receiver flag")
	}
	if !strings.Contains(err.Error(), "sender account") {
		t.Fatalf("error should name the sender, got %q", err)
	}
}

// Neither side present: still a sender failure, and it must be reported as one.
func TestBothAbsentReportsSenderWithFlagOn(t *testing.T) {
	withAllowNewReceivers(t, true)

	sender, receiver := addrOf(senderHex), addrOf(newReceiverHex)
	c := cacheWith(t) // empty cache

	if ok, err := c.CheckAddressExistWithCache(transfer(sender, receiver), context.Background()); ok || err == nil {
		t.Fatal("an absent sender must be rejected even when new receivers are allowed")
	} else if !strings.Contains(err.Error(), "sender account") {
		t.Fatalf("error should name the sender, got %q", err)
	}
}

// A nil From is malformed regardless of flag state.
func TestNilSenderRejectedUnderBothFlagStates(t *testing.T) {
	for _, on := range []bool{false, true} {
		withAllowNewReceivers(t, on)
		c := cacheWith(t)
		if ok, err := c.CheckAddressExistWithCache(transfer(nil, addrOf(newReceiverHex)), context.Background()); ok || err == nil {
			t.Fatalf("nil sender must be rejected (flag=%v)", on)
		}
	}
}

// Contract deployment (To == nil) only ever required the sender; unaffected.
func TestContractDeploymentUnaffected(t *testing.T) {
	for _, on := range []bool{false, true} {
		withAllowNewReceivers(t, on)
		sender := addrOf(senderHex)
		c := cacheWith(t, *sender)
		ok, err := c.CheckAddressExistWithCache(transfer(sender, nil), context.Background())
		if err != nil || !ok {
			t.Fatalf("contract deployment must pass with a known sender (flag=%v): ok=%v err=%v", on, ok, err)
		}
	}
}

// Both parties known: accepted either way, so enabling the flag cannot regress
// the ordinary case.
func TestKnownReceiverUnaffected(t *testing.T) {
	for _, on := range []bool{false, true} {
		withAllowNewReceivers(t, on)
		sender, receiver := addrOf(senderHex), addrOf(newReceiverHex)
		c := cacheWith(t, *sender, *receiver)
		ok, err := c.CheckAddressExistWithCache(transfer(sender, receiver), context.Background())
		if err != nil || !ok {
			t.Fatalf("known sender+receiver must pass (flag=%v): ok=%v err=%v", on, ok, err)
		}
	}
}

// Admission must still be gated on the SENDER's balance. A new receiver does
// not make an unfunded transfer acceptable.
func TestNewReceiverStillRequiresSenderFunds(t *testing.T) {
	withAllowNewReceivers(t, true)

	sender, receiver := addrOf(senderHex), addrOf(newReceiverHex)
	c := NewSecurityCache()
	c.RegisterAccount(*sender, &DB_OPs.Account{Address: *sender, Balance: "1", TxNonce: 0})

	tx := transfer(sender, receiver)
	if ok, err := c.CheckAddressExistWithCache(tx, context.Background()); !ok || err != nil {
		t.Fatalf("existence check should pass: ok=%v err=%v", ok, err)
	}
	ok, err := c.CheckBalanceWithCache(tx, context.Background())
	if ok && err == nil {
		t.Fatal("a sender with 1 wei must not be able to send 1e14 just because the receiver is new")
	}
}

// The default must be ON: sending to a first-time address is the intended
// behaviour, so an operator who configures nothing gets it. The old rule stays
// reachable as a kill switch.
func TestFlagDefaultsOnWithKillSwitch(t *testing.T) {
	// The shipped default, read with the variable unset.
	if _, set := os.LookupEnv("JMDN_ALLOW_NEW_RECEIVER_ACCOUNTS"); !set {
		if !AllowNewReceiverAccounts {
			t.Fatal("AllowNewReceiverAccounts must default to true")
		}
	}
	if !envOn("JMDN_ALLOW_NEW_RECEIVER_ACCOUNTS_TEST_ONLY_UNSET", true) {
		t.Fatal("envOn must honour its default when the variable is unset")
	}

	// Kill switch: every documented off-value must restore the old rule.
	for _, off := range []string{"0", "false", "no", "off", "OFF", " off "} {
		t.Setenv("JMDN_ALLOW_NEW_RECEIVER_ACCOUNTS", off)
		if envOn("JMDN_ALLOW_NEW_RECEIVER_ACCOUNTS", true) {
			t.Fatalf("%q must disable the rule (kill switch)", off)
		}
	}
	t.Setenv("JMDN_ALLOW_NEW_RECEIVER_ACCOUNTS", "1")
	if !envOn("JMDN_ALLOW_NEW_RECEIVER_ACCOUNTS", true) {
		t.Fatal("=1 must keep the rule enabled")
	}
}
