//go:build applygate

package BlockProcessing_test

// A failed block must leave no trace on the node that applied it.
//
//	APPLYGATE_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
//	  CGO_ENABLED=1 go test -tags applygate ./messaging/BlockProcessing/ -run 'TestRollback_' -v

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
	"gossipnode/messaging/BlockProcessing"
)

// Value transfers only: tx1 pays a brand-new account, tx2 has a stale nonce, so
// the whole block fails. The new account must not exist afterwards (not even
// with a zero balance: the state fingerprint folds every account) and the
// sender is restored, including the ART identity tx1 adopted. Run with the
// group commit on and off.
func TestRollback_NewRecipientIsRemoved(t *testing.T) {
	for _, grouped := range []bool{true, false} {
		t.Run(map[bool]string{true: "grouped", false: "per-tx"}[grouped], func(t *testing.T) {
			defer BlockProcessing.SetGroupedApplyForTest(grouped, 0)()
			cleanup := buildHandle(t, t.TempDir())
			defer cleanup()
			seedGenesis(t)

			fresh := freshAddr("rollback-new-recipient")
			before := view(t, acctA)
			blk := sequencerBlock(t, 1, common.Hash{}, []config.Transaction{
				callTx(acctA, fresh, 0, nil, big.NewInt(5)),
				callTx(acctA, acctB, 0, nil, big.NewInt(7)), // nonce 0 again: stale
			}, false)
			// Carry a different ART identity for the sender, so tx1 adopts it
			// (identity heal) before tx2 fails the block: the rollback must
			// restore the original.
			for i := range blk.AccountNonces {
				if blk.AccountNonces[i].Address == acctA {
					blk.AccountNonces[i].Nonce = before.art + 1000
				}
			}
			if err := BlockProcessing.ProcessBlockTransactions(context.Background(), blk, nil); err == nil {
				t.Fatal("precondition: the block must fail on its stale-nonce tx")
			}
			if v := view(t, fresh); v.exists {
				t.Fatalf("the failed block's new recipient exists after rollback (balance %s)", v.balance)
			}
			if after := view(t, acctA); after != before {
				t.Fatalf("sender not restored: before %+v after %+v", before, after)
			}
		})
	}
}

// The reported sequence: Forward.pay(Z){10} succeeds, then a later contract tx
// of the same block creates an account with no carried identity and the block
// fails. The contract call committed storage, which nothing can undo, so the
// rollback is refused and block apply halts: Z is paid once, not once per
// retry, and no block is applied on top of the partial state.
func TestRollback_ContractPrefixRefusesAndHalts(t *testing.T) {
	setRevertHeight(t, 0)
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)

	fwd := contractAddr(acctA, 0)
	z, fresh := freshAddr("rollback-z"), freshAddr("rollback-fresh")
	b1 := sequencerBlock(t, 1, common.Hash{}, []config.Transaction{deployTx(acctA, 0, mustHex(t, binForward))}, true)
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b1, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	poison := sequencerBlock(t, 2, b1.BlockHash, []config.Transaction{
		callTx(acctA, fwd, 1, calldata("pay(address)", z.Bytes()), big.NewInt(10)),
		callTx(acctA, fwd, 2, nil, big.NewInt(100)),
		callTx(acctA, fwd, 3, calldata("payout(address,uint256)", fresh.Bytes(), big.NewInt(40).Bytes()), big.NewInt(0)),
	}, true)

	for attempt := 1; attempt <= 3; attempt++ {
		err := BlockProcessing.ProcessBlockTransactions(context.Background(), poison, nil)
		if !errors.Is(err, BlockProcessing.ErrApplyHalted) {
			t.Fatalf("attempt %d: want ErrApplyHalted, got %v", attempt, err)
		}
		if v := view(t, z); !v.exists || v.balance != "10" {
			t.Fatalf("attempt %d: Z = %+v, want exactly one payment of 10", attempt, v)
		}
	}
	if BlockProcessing.ApplyHaltReason() == "" {
		t.Fatal("no halt reason recorded")
	}

	clean := sequencerBlock(t, 2, b1.BlockHash, []config.Transaction{callTx(acctB, acctA, 0, nil, big.NewInt(1))}, false)
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), clean, nil); !errors.Is(err, BlockProcessing.ErrApplyHalted) {
		t.Fatalf("a block on top of the partial state must be refused, got %v", err)
	}
}
