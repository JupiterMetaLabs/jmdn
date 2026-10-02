//go:build applygate

// Apply-gate tests for accounts created DURING EVM execution (internal
// CALL{value}, SELFDESTRUCT beneficiary, value-funded CREATE/CREATE2) and for
// the post-activation deterministic-revert backstop.
//
// Topology per test: store A is the SEQUENCER (predict → enrich → apply, stamping
// the state fingerprint); store B is an independent VALIDATOR that replays A's
// exact blocks. B verifies A's fingerprint on every block (identical state root,
// accounts + contract storage) and the tests additionally compare each new
// account's ART identity, which the fingerprint does not cover.
//
//	APPLYGATE_PG_DSN="host=127.0.0.1 user=postgres sslmode=disable" \
//	APPLYGATE_DEPLOY_BYTECODE=<SimpleStorage bin> \
//	CGO_ENABLED=1 go test -tags applygate ./messaging/BlockProcessing/ -run TestInternalCreate -v

package BlockProcessing_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/DB_OPs"
	"gossipnode/Security"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/execbridge"
	"gossipnode/messaging/BlockProcessing"
)

// ── tx / calldata helpers ────────────────────────────────────────────────────

func mustHex(t *testing.T, h string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(h, "0x"))
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return b
}

func sel(sig string) []byte { return crypto.Keccak256([]byte(sig))[:4] }

func word(b []byte) []byte { return common.LeftPadBytes(b, 32) }

func calldata(sig string, args ...[]byte) []byte {
	out := append([]byte{}, sel(sig)...)
	for _, a := range args {
		out = append(out, word(a)...)
	}
	return out
}

func freshAddr(tag string) common.Address {
	return common.BytesToAddress(crypto.Keccak256([]byte("fresh:" + tag))[12:])
}

func deployValueTx(sender common.Address, nonce uint64, code []byte, value *big.Int) config.Transaction {
	tx := deployTx(sender, nonce, code)
	tx.Value = value
	return tx
}

// ── sequencer / validator drivers ────────────────────────────────────────────

// sequencerBlock builds a block the way Block/Server.go processZKBlock now does:
// predict execution-touched accounts, then enrich in one ordinal pass. With
// predict=false it reproduces the PRE-FIX sequencer (tx-level stamping only).
func sequencerBlock(t *testing.T, num uint64, prev common.Hash, txs []config.Transaction, predict bool) *config.ZKBlock {
	t.Helper()
	cb, zk := acctA, acctB
	blk := &config.ZKBlock{
		Transactions: txs,
		Timestamp:    int64(1_700_000_000 + num),
		CoinbaseAddr: &cb,
		ZKVMAddr:     &zk,
		PrevHash:     prev,
		BlockNumber:  num,
		GasLimit:     30_000_000,
	}
	blk.BlockHash = Security.RecomputeBlockHashFromContents(txs)
	var predicted []common.Address
	if predict {
		var err error
		predicted, err = BlockProcessing.PredictContractCreatedAccounts(context.Background(), blk)
		if err != nil {
			t.Fatalf("PredictContractCreatedAccounts(block %d): %v", num, err)
		}
	}
	if err := DB_OPs.EnrichBlockAccountNoncesWithPredicted(blk, predicted); err != nil {
		t.Fatalf("enrich(block %d): %v", num, err)
	}
	return blk
}

type acctView struct {
	exists  bool
	balance string
	art     uint64
	txNonce uint64
}

func view(t *testing.T, a common.Address) acctView {
	t.Helper()
	d, err := DB_OPs.GetAccount(nil, a)
	if err != nil {
		if DB_OPs.IsNotFound(err) {
			return acctView{}
		}
		t.Fatalf("GetAccount(%s): %v", a.Hex(), err)
	}
	b := d.Balance
	if b == "" {
		b = "0"
	}
	return acctView{exists: true, balance: b, art: d.Nonce, txNonce: d.TxNonce}
}

// runSequencerThenValidator produces every block on store A (sequencer), then
// replays the stamped blocks on an independent store B (validator) and returns
// both stores' views of `watch`. blocks[i] are built after blocks[i-1] applied on
// A, so prediction sees the committed pre-block state exactly as live.
func runSequencerThenValidator(t *testing.T, blocks [][]config.Transaction, predict bool, watch []common.Address) (a, b map[common.Address]acctView, stamped []*config.ZKBlock) {
	t.Helper()
	a = map[common.Address]acctView{}
	b = map[common.Address]acctView{}

	cleanupA := buildHandle(t, t.TempDir())
	seedGenesis(t)
	prev := common.Hash{}
	for i, txs := range blocks {
		blk := sequencerBlock(t, uint64(i+1), prev, txs, predict)
		if err := BlockProcessing.ProcessBlockTransactions(context.Background(), blk, nil); err != nil {
			cleanupA()
			t.Fatalf("SEQUENCER apply block %d: %v", blk.BlockNumber, err)
		}
		if blk.StateFingerprint == "" {
			cleanupA()
			t.Fatalf("sequencer did not stamp a state fingerprint on block %d", blk.BlockNumber)
		}
		stamped = append(stamped, blk)
		prev = blk.BlockHash
	}
	for _, w := range watch {
		a[w] = view(t, w)
	}
	cleanupA()
	dumpStampedBlocks(t, stamped)

	cleanupB := buildHandle(t, t.TempDir())
	defer cleanupB()
	seedGenesis(t)
	for _, blk := range stamped {
		if err := BlockProcessing.ProcessBlockTransactions(context.Background(), blk, nil); err != nil {
			t.Fatalf("VALIDATOR diverged/failed on block %d: %v", blk.BlockNumber, err)
		}
	}
	for _, w := range watch {
		b[w] = view(t, w)
	}
	return a, b, stamped
}

// dumpStampedBlocks writes the sequencer-stamped blocks as wire JSON to
// $APPLYGATE_DUMP_DIR/<test>.json, so a validator on a DIFFERENT build (e.g. the
// pre-fix release) can replay them — the mixed-fleet compatibility check.
func dumpStampedBlocks(t *testing.T, blocks []*config.ZKBlock) {
	t.Helper()
	dir := os.Getenv("APPLYGATE_DUMP_DIR")
	if dir == "" {
		return
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal stamped blocks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, t.Name()+".json"), raw, 0o644); err != nil {
		t.Fatalf("write stamped blocks: %v", err)
	}
}

// assertCreated: account exists on both stores with the wanted balance, a real
// (non-zero) ART identity, and the SAME identity on sequencer and validator.
func assertCreated(t *testing.T, label string, addr common.Address, want *big.Int, a, b map[common.Address]acctView) {
	t.Helper()
	va, vb := a[addr], b[addr]
	if !va.exists || !vb.exists {
		t.Fatalf("%s %s: account not created (sequencer=%v validator=%v)", label, addr.Hex(), va.exists, vb.exists)
	}
	if va.balance != want.String() || vb.balance != want.String() {
		t.Fatalf("%s %s: balance sequencer=%s validator=%s want %s", label, addr.Hex(), va.balance, vb.balance, want)
	}
	if va.art == 0 || va.art >= DB_OPs.ARTOrdinalMax {
		t.Fatalf("%s %s: ART identity %d is not a sequencer ordinal", label, addr.Hex(), va.art)
	}
	if va.art != vb.art {
		t.Fatalf("%s %s: ART identity differs sequencer=%d validator=%d", label, addr.Hex(), va.art, vb.art)
	}
	t.Logf("%s %s: balance=%s art=%d (sequencer == validator)", label, addr.Hex(), va.balance, va.art)
}

func setRevertHeight(t *testing.T, h uint64) {
	t.Helper()
	cfg := settings.Get()
	old := cfg.Consensus.EVMUnstampedAccountRevertHeight
	cfg.Consensus.EVMUnstampedAccountRevertHeight = h
	t.Cleanup(func() { cfg.Consensus.EVMUnstampedAccountRevertHeight = old })
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestInternalCreate_ReproLegacy reproduces the testnet failure: a pre-fix
// sequencer (no execution prediction) stamps only From/To, so Forward.pay(fresh)
// fails the WHOLE block with the exact production error.
func TestInternalCreate_ReproLegacy(t *testing.T) {
	setRevertHeight(t, 0)
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)

	fwd := contractAddr(acctA, 0)
	fresh := freshAddr("repro")
	b1 := sequencerBlock(t, 1, common.Hash{}, []config.Transaction{deployTx(acctA, 0, mustHex(t, binForward))}, false)
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b1, nil); err != nil {
		t.Fatalf("deploy Forward: %v", err)
	}
	b2 := sequencerBlock(t, 2, b1.BlockHash, []config.Transaction{
		callTx(acctA, fwd, 1, calldata("pay(address)", fresh.Bytes()), big.NewInt(1)),
	}, false)
	err := BlockProcessing.ProcessBlockTransactions(context.Background(), b2, nil)
	if err == nil {
		t.Fatal("expected the legacy whole-block failure, got success")
	}
	want := "new account " + fresh.Hex() + " has no block-carried ART identity"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("unexpected error: %v (want substring %q)", err, want)
	}
	t.Logf("REPRODUCED: %v", err)
}

// TestInternalCreate_ForwardPay: Forward.pay(fresh){1 wei} and
// payTwo(fresh1, fresh2){3 wei} create new accounts with identities, identical
// on sequencer and validator.
func TestInternalCreate_ForwardPay(t *testing.T) {
	fwd := contractAddr(acctA, 0)
	f0, f1, f2 := freshAddr("pay"), freshAddr("two-a"), freshAddr("two-b")
	blocks := [][]config.Transaction{
		{deployTx(acctA, 0, mustHex(t, binForward))},
		{callTx(acctA, fwd, 1, calldata("pay(address)", f0.Bytes()), big.NewInt(1))},
		{callTx(acctA, fwd, 2, calldata("payTwo(address,address)", f1.Bytes(), f2.Bytes()), big.NewInt(3))},
	}
	a, b, _ := runSequencerThenValidator(t, blocks, true, []common.Address{f0, f1, f2})
	assertCreated(t, "pay", f0, big.NewInt(1), a, b)
	assertCreated(t, "payTwo[a]", f1, big.NewInt(1), a, b)
	assertCreated(t, "payTwo[b]", f2, big.NewInt(2), a, b)
	if a[f1].art == a[f2].art {
		t.Fatalf("two fresh accounts in one tx share ART identity %d", a[f1].art)
	}
}

// TestInternalCreate_CreateCreate2: value-funded CREATE and CREATE2 children
// inside a contract get a ledger account with an identity; a zero-value CREATE
// still succeeds.
func TestInternalCreate_CreateCreate2(t *testing.T) {
	fac := contractAddr(acctA, 0)
	child1 := crypto.CreateAddress(fac, 1) // factory nonce starts at 1 (EIP-161)
	salt := common.HexToHash("0x5a17")
	child2 := crypto.CreateAddress2(fac, salt, crypto.Keccak256(mustHex(t, binChild)))
	child3 := crypto.CreateAddress(fac, 3) // after CREATE (nonce 1→2) and CREATE2 (2→3)
	blocks := [][]config.Transaction{
		{deployTx(acctA, 0, mustHex(t, binFactory))},
		{callTx(acctA, fac, 1, calldata("create()"), big.NewInt(5))},
		{callTx(acctA, fac, 2, calldata("create2(bytes32)", salt.Bytes()), big.NewInt(6))},
		{callTx(acctA, fac, 3, calldata("create()"), big.NewInt(0))},
	}
	a, b, _ := runSequencerThenValidator(t, blocks, true, []common.Address{child1, child2, child3})
	assertCreated(t, "CREATE child", child1, big.NewInt(5), a, b)
	assertCreated(t, "CREATE2 child", child2, big.NewInt(6), a, b)
	// Zero-value child: no value moved, so no ledger account is required; the
	// point is that the tx applied (runSequencerThenValidator would have failed).
	t.Logf("zero-value CREATE child %s ledger account exists=%v (no value moved)", child3.Hex(), a[child3].exists)
}

// TestInternalCreate_SelfdestructBeneficiary: SELFDESTRUCT to a fresh address
// moves the contract balance into a newly created account.
func TestInternalCreate_SelfdestructBeneficiary(t *testing.T) {
	bomb := contractAddr(acctA, 0)
	ben := freshAddr("beneficiary")
	blocks := [][]config.Transaction{
		{deployValueTx(acctA, 0, mustHex(t, binBomb), big.NewInt(7))},
		{callTx(acctA, bomb, 1, calldata("boom(address)", ben.Bytes()), big.NewInt(0))},
	}
	a, b, _ := runSequencerThenValidator(t, blocks, true, []common.Address{ben, bomb})
	assertCreated(t, "selfdestruct beneficiary", ben, big.NewInt(7), a, b)
	if a[bomb].balance != "0" || b[bomb].balance != "0" {
		t.Fatalf("bomb balance not drained: sequencer=%s validator=%s", a[bomb].balance, b[bomb].balance)
	}
}

// TestInternalCreate_SuperJRegisterShape mirrors DIDRegistry.registerDID →
// SettlementContract.transferReward (inside try/catch) paying a never-used
// wallet 1e18: WelcomePaid is emitted and the wallet holds exactly 1e18.
func TestInternalCreate_SuperJRegisterShape(t *testing.T) {
	one := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	settle := contractAddr(acctA, 0)
	reg := contractAddr(acctA, 1)
	wallet := freshAddr("superj-wallet")
	regCode := append(mustHex(t, binRegistry), word(settle.Bytes())...)
	registerTx := callTx(acctA, reg, 2, calldata("register(address,uint256)", wallet.Bytes(), one.Bytes()), big.NewInt(0))
	blocks := [][]config.Transaction{
		{deployValueTx(acctA, 0, mustHex(t, binSettlement), new(big.Int).Mul(one, big.NewInt(10)))},
		{deployTx(acctA, 1, regCode)},
		{registerTx},
	}
	a, b, stamped := runSequencerThenValidator(t, blocks, true, []common.Address{wallet})
	assertCreated(t, "SuperJ welcome wallet", wallet, one, a, b)

	// WelcomePaid emitted: re-execute the register tx against the validator's
	// PRE-block state is not possible post-apply, so check the stamped block's
	// carried identity for the wallet AND the event via a fresh simulation on a
	// store holding blocks 1-2 only.
	carried := false
	for _, an := range stamped[2].AccountNonces {
		if an.Address == wallet && an.Nonce != 0 {
			carried = true
		}
	}
	if !carried {
		t.Fatal("register block does not carry an ART identity for the wallet")
	}
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)
	for _, blk := range stamped[:2] {
		if err := BlockProcessing.ProcessBlockTransactions(context.Background(), blk, nil); err != nil {
			t.Fatalf("replay setup block %d: %v", blk.BlockNumber, err)
		}
	}
	res, err := execbridge.Get().ExecuteTx(context.Background(), &registerTx, execbridge.BlockExecContext{
		ChainID: settings.Get().Network.ChainID, BlockNumber: 3, BlockHash: stamped[2].BlockHash,
		Time: stamped[2].Timestamp, Coinbase: acctA, TxIndex: 0, GasLimit: 30_000_000,
	})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("simulate register: err=%v res=%+v", err, res)
	}
	topic := crypto.Keccak256Hash([]byte("WelcomePaid(address,uint256)"))
	found := false
	for _, l := range res.Logs {
		if l != nil && l.Address == reg && len(l.Topics) == 2 && l.Topics[0] == topic && common.BytesToAddress(l.Topics[1].Bytes()) == wallet {
			found = true
		}
	}
	if !found {
		t.Fatalf("WelcomePaid(%s) not emitted; logs=%d", wallet.Hex(), len(res.Logs))
	}
	t.Logf("WelcomePaid(%s, 1e18) emitted by Registry %s", wallet.Hex(), reg.Hex())
}

// intraBlockDependency: block 2 = [fund Forward (contract tx), Forward.payout(fresh)
// from that NEW balance, plain transfer acctA→acctB]. Prediction simulates
// payout against the pre-block state (Forward balance 0) → it reverts there, so
// `fresh` is NOT stamped; at apply the payout succeeds in the EVM and creates an
// unstamped account.
func intraBlockDependency(t *testing.T) (blocks [][]config.Transaction, fwd, fresh common.Address) {
	fwd = contractAddr(acctA, 0)
	fresh = freshAddr("intra-block")
	blocks = [][]config.Transaction{
		{deployTx(acctA, 0, mustHex(t, binForward))},
		{
			callTx(acctA, fwd, 1, nil, big.NewInt(100)), // receive()
			callTx(acctA, fwd, 2, calldata("payout(address,uint256)", fresh.Bytes(), big.NewInt(40).Bytes()), big.NewInt(0)),
			callTx(acctA, acctB, 3, nil, big.NewInt(9)), // unrelated value transfer
		},
	}
	return blocks, fwd, fresh
}

// TestInternalCreate_BackstopRevertsOnlyTheTx (post-activation): the unstampable
// tx is applied as a REVERT (no value moved, fresh not created, sender nonce
// bumped, gas charged); the other txs of the block apply; sequencer and
// validator agree on the state fingerprint.
func TestInternalCreate_BackstopRevertsOnlyTheTx(t *testing.T) {
	setRevertHeight(t, 1)
	blocks, fwd, fresh := intraBlockDependency(t)
	a, b, stamped := runSequencerThenValidator(t, blocks, true, []common.Address{fwd, fresh, acctA})
	for _, an := range stamped[1].AccountNonces {
		if an.Address == fresh {
			t.Fatalf("precondition: prediction unexpectedly stamped %s (intra-block dependency not exercised)", fresh.Hex())
		}
	}
	if a[fresh].exists || b[fresh].exists {
		t.Fatalf("fresh account was created despite the revert (sequencer=%v validator=%v)", a[fresh].exists, b[fresh].exists)
	}
	if a[fwd].balance != "100" || b[fwd].balance != "100" {
		t.Fatalf("Forward balance: sequencer=%s validator=%s want 100 (funding tx applied, payout reverted)", a[fwd].balance, b[fwd].balance)
	}
	if a[acctA].txNonce != 4 || b[acctA].txNonce != 4 {
		t.Fatalf("sender tx nonce: sequencer=%d validator=%d want 4 (reverted tx still consumes its nonce)", a[acctA].txNonce, b[acctA].txNonce)
	}
	t.Logf("PASS: payout reverted deterministically; block applied; fingerprints matched on validator")
}

// TestInternalCreate_BackstopOffKeepsLegacy (pre-activation): the same block
// fails as a whole exactly as before — the rule change is height-gated.
func TestInternalCreate_BackstopOffKeepsLegacy(t *testing.T) {
	setRevertHeight(t, 0)
	blocks, _, fresh := intraBlockDependency(t)
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)
	b1 := sequencerBlock(t, 1, common.Hash{}, blocks[0], true)
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b1, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	b2 := sequencerBlock(t, 2, b1.BlockHash, blocks[1], true)
	err := BlockProcessing.ProcessBlockTransactions(context.Background(), b2, nil)
	if err == nil || !strings.Contains(err.Error(), fresh.Hex()+" has no block-carried ART identity") {
		t.Fatalf("pre-activation must keep the legacy whole-block failure, got: %v", err)
	}
	t.Logf("PASS (legacy unchanged below activation): %v", err)
}

// TestInternalCreate_PredictionHasNoSideEffects: predicting a block commits
// nothing — balances and contract storage are untouched and the block still
// applies normally afterwards.
func TestInternalCreate_PredictionHasNoSideEffects(t *testing.T) {
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)
	fwd := contractAddr(acctA, 0)
	fresh := freshAddr("no-side-effects")
	b1 := sequencerBlock(t, 1, common.Hash{}, []config.Transaction{deployTx(acctA, 0, mustHex(t, binForward))}, true)
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b1, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	fpBefore, err := DB_OPs.ComputeAccountStateFingerprintV1(context.Background())
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	blk := &config.ZKBlock{
		Transactions: []config.Transaction{callTx(acctA, fwd, 1, calldata("pay(address)", fresh.Bytes()), big.NewInt(1))},
		Timestamp:    1_700_000_002, CoinbaseAddr: &acctA, ZKVMAddr: &acctB, BlockNumber: 2,
	}
	for i := 0; i < 3; i++ {
		got, err := BlockProcessing.PredictContractCreatedAccounts(context.Background(), blk)
		if err != nil {
			t.Fatalf("predict: %v", err)
		}
		hasFresh := false
		for _, a := range got {
			hasFresh = hasFresh || a == fresh
		}
		if !hasFresh {
			t.Fatalf("prediction %d missed the internal-transfer recipient: %v", i, got)
		}
	}
	fpAfter, err := DB_OPs.ComputeAccountStateFingerprintV1(context.Background())
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if fpBefore != fpAfter {
		t.Fatalf("prediction mutated state: %s -> %s", fpBefore, fpAfter)
	}
	if v := view(t, fresh); v.exists {
		t.Fatal("prediction created the recipient account")
	}
}
