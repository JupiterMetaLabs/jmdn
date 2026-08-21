//go:build applygate

// Apply-path determinism gate (audit P2.5 / EVM). Proves that two INDEPENDENT
// ThebeDB stores, given the SAME genesis and the SAME blocks, derive BYTE-IDENTICAL
// state (accounts + contract storage + fingerprint), and that a perturbed store
// HALTS instead of serving divergent state.
//
// It bypasses the whole sequencer pipeline (seed/mempool/MRE/orchestrator/
// Espresso/ZKVM) and drives the real apply entry point ProcessBlockTransactions
// directly against two stores in one process (the store is selected by the
// process-wide DB_OPs global handle, so the two runs are sequential: build store
// A, seed+apply, capture; then store B, seed+apply, capture; compare).
//
// HOST-GATED (CGO + real go-ethereum + ThebeDB):
//
//	CGO_ENABLED=1 go test -tags applygate ./messaging/BlockProcessing/ -run TestApplyGate -v
//
// Provide the contract deploy bytecode (compile local-2node-gate/contracts/
// SimpleStorage.sol with solc, or reuse the driver's output):
//
//	export APPLYGATE_DEPLOY_BYTECODE=0x60806040...   # SimpleStorage creation bytecode
//
// NOTE: this file is written against the wiring in main.go (ThebeDB init) and the
// apply path in Processing.go. Because it can't be compiled in the authoring
// sandbox, expect to reconcile a few imports/signatures on first `go test` — the
// likely spots are called out in local-2node-gate/APPLY-HARNESS.md.

package BlockProcessing_test

import (
	"context"
	"encoding/hex"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/builder"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/kv"
	"github.com/JupiterMetaLabs/ThebeDB/pkg/profile"
	thebeSql "github.com/JupiterMetaLabs/ThebeDB/pkg/sql"

	"go.uber.org/zap"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/backend"
	"gossipnode/DB_OPs/cassata"
	"gossipnode/DB_OPs/contractDB"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/DB_OPs/thebeprofile"
	"gossipnode/Security"
	"gossipnode/SmartContract/evmexec"
	"gossipnode/config"
	"gossipnode/consensushash"
	"gossipnode/messaging/BlockProcessing"
)

const chainID = 8000800

// two funded genesis accounts (deterministic addresses; balances in wei)
var (
	acctA = common.HexToAddress("0x00000000000000000000000000000000000000A1")
	acctB = common.HexToAddress("0x00000000000000000000000000000000000000B2")
	oneK  = new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1e18)) // 1e6 ETH
)

// buildHandle stands up a real ThebeDB handle in `dir`, installs it as the
// process-wide handle, and registers the EVM executor + contract fold hook against
// it. Returns a cleanup. Mirrors main.go's ThebeDB init (contracts-enabled path).
func buildHandle(t *testing.T, dir string) func() {
	t.Helper()

	reg := profile.NewRegistry()
	reg.Register(thebeprofile.NewJMDNProfile())

	kvStore, err := kv.NewStore(kv.Config{Backend: kv.BackendBadger, Path: filepath.Join(dir, "kv")})
	if err != nil {
		t.Fatalf("kv.NewStore: %v", err)
	}

	// SQLite projection for a local test (no Postgres needed). Adjust the DSN form
	// if thebeSql.NewSQLEngine expects a driver prefix in your build.
	sqlEngine, err := thebeSql.NewSQLEngine("file:" + filepath.Join(dir, "sql.db") + "?_foreign_keys=on")
	if err != nil {
		t.Fatalf("thebeSql.NewSQLEngine: %v", err)
	}

	db, err := thebedb.New(kvStore, sqlEngine, thebedb.WithProfileRegistry(reg))
	if err != nil {
		t.Fatalf("thebedb.New: %v", err)
	}

	cas := cassata.New(db, zap.NewNop())

	// EVM execution against this store's local ledger (EVM-A16) + P4 contract fold.
	evmexec.Register(
		chainID,
		DB_OPs.ContractAccountSource{},
		contractDB.NewKVStateRepository(cas.KV(), cas),
		contractDB.HasCode,
	)
	kvForFold := cas.KV()
	DB_OPs.SetContractFoldHook(func(f *consensushash.StateFingerprinterV1) error {
		return contractDB.FoldAllContracts(kvForFold, f)
	})

	outbox, err := thebegateway.NewOutboxStore(filepath.Join(dir, "kv", "outbox.db"))
	if err != nil {
		t.Fatalf("NewOutboxStore: %v", err)
	}
	gw := thebegateway.NewThebeGateway(builder.New(db), db.KV, nil, outbox)
	reader := thebegateway.NewThebeReader(db.SQL.GetDB(), db.KV, nil)
	handle := backend.NewComposite(backend.New(gw, reader, nil), nil)

	DB_OPs.SetGlobalHandle(handle)

	// Allow out-of-band account creation for genesis seeding in-test.
	t.Setenv("JMDN_ALLOW_LOCAL_ACCOUNT_CREATE", "1")

	return func() {
		DB_OPs.SetGlobalHandle(nil)
		_ = db.Close()
	}
}

// seedGenesis funds the two accounts on the currently-installed handle.
func seedGenesis(t *testing.T) {
	t.Helper()
	for _, a := range []common.Address{acctA, acctB} {
		if err := DB_OPs.CreateAccount(nil, "did:jmdn:"+strings.ToLower(a.Hex()), a, nil); err != nil {
			t.Fatalf("CreateAccount(%s): %v", a.Hex(), err)
		}
		if err := DB_OPs.UpdateAccountBalance(nil, a, oneK.String(), 0); err != nil {
			t.Fatalf("UpdateAccountBalance(%s): %v", a.Hex(), err)
		}
	}
}

// setSelector returns the calldata for SimpleStorage.set(uint256 v).
func setCalldata(v uint64) []byte {
	sel := crypto.Keccak256([]byte("set(uint256)"))[:4]
	arg := common.LeftPadBytes(new(big.Int).SetUint64(v).Bytes(), 32)
	return append(append([]byte{}, sel...), arg...)
}

// deployBytecode reads the SimpleStorage creation bytecode from the environment.
func deployBytecode(t *testing.T) []byte {
	t.Helper()
	h := strings.TrimPrefix(strings.TrimSpace(os.Getenv("APPLYGATE_DEPLOY_BYTECODE")), "0x")
	if h == "" {
		t.Skip("set APPLYGATE_DEPLOY_BYTECODE to the SimpleStorage creation bytecode (compile contracts/SimpleStorage.sol with solc)")
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("bad APPLYGATE_DEPLOY_BYTECODE hex: %v", err)
	}
	return b
}

// contractAddr is the CREATE address for a deploy from `sender` at `nonce`.
func contractAddr(sender common.Address, nonce uint64) common.Address {
	return crypto.CreateAddress(sender, nonce)
}

// makeBlock assembles a ZKBlock, stamps the ART identity nonces, and computes the
// canonical block hash. StateFingerprint is left empty so the FIRST apply stamps
// it (producer role); subsequent applies verify against it.
func makeBlock(t *testing.T, num uint64, prev common.Hash, txs []config.Transaction) *config.ZKBlock {
	t.Helper()
	cb := acctA
	zk := acctB
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
	if err := DB_OPs.EnrichBlockAccountNonces(blk); err != nil {
		t.Fatalf("EnrichBlockAccountNonces(block %d): %v", num, err)
	}
	return blk
}

func deployTx(sender common.Address, nonce uint64, code []byte) config.Transaction {
	h := crypto.Keccak256Hash(append([]byte("deploy"), append(sender.Bytes(), byte(nonce))...))
	return config.Transaction{
		Hash:     h,
		From:     &sender,
		To:       nil, // contract creation
		Value:    big.NewInt(0),
		Type:     0,
		ChainID:  big.NewInt(chainID),
		Nonce:    nonce,
		GasLimit: 3_000_000,
		GasPrice: big.NewInt(1),
		Data:     code,
	}
}

func callTx(sender, to common.Address, nonce uint64, data []byte, value *big.Int) config.Transaction {
	h := crypto.Keccak256Hash(append([]byte("call"), append(append(sender.Bytes(), to.Bytes()...), byte(nonce))...))
	return config.Transaction{
		Hash:     h,
		From:     &sender,
		To:       &to,
		Value:    value,
		Type:     0,
		ChainID:  big.NewInt(chainID),
		Nonce:    nonce,
		GasLimit: 1_000_000,
		GasPrice: big.NewInt(1),
		Data:     data,
	}
}

// produceOnA builds the blocks WHILE store A's handle is live (EnrichBlockAccountNonces
// needs an active store), applies them so A STAMPS each block's fingerprint, and
// returns the stamped blocks for replay on B. Building+applying incrementally so the
// call block is enriched after the contract exists.
func produceOnA(t *testing.T, dir string, code []byte) []*config.ZKBlock {
	t.Helper()
	cleanup := buildHandle(t, dir)
	defer cleanup()
	seedGenesis(t)

	deployer := acctA
	cAddr := contractAddr(deployer, 0)

	b1 := makeBlock(t, 1, common.Hash{}, []config.Transaction{deployTx(deployer, 0, code)})
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b1, nil); err != nil {
		t.Fatalf("A apply deploy: %v", err)
	}
	b2 := makeBlock(t, 2, b1.BlockHash, []config.Transaction{callTx(deployer, cAddr, 1, setCalldata(42), big.NewInt(0))})
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b2, nil); err != nil {
		t.Fatalf("A apply call: %v", err)
	}
	if b1.StateFingerprint == "" || b2.StateFingerprint == "" {
		t.Fatal("store A did not stamp fingerprints")
	}
	return []*config.ZKBlock{b1, b2}
}

// TestApplyGate_Determinism: store B, seeded identically, replays the exact blocks
// A stamped. The P2.5 fingerprint commits accounts AND contract storage, so B
// recomputes and compares against A's stamp on every block — if B diverges in any
// way, ProcessBlockTransactions returns a halt error. Success == identical state.
func TestApplyGate_Determinism(t *testing.T) {
	code := deployBytecode(t)

	blocks := produceOnA(t, t.TempDir(), code)

	// Replay on an independent store B.
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)
	for _, b := range blocks {
		if err := BlockProcessing.ProcessBlockTransactions(context.Background(), b, nil); err != nil {
			t.Fatalf("store B diverged applying block %d (fingerprint mismatch): %v", b.BlockNumber, err)
		}
	}
	t.Logf("PASS: store B independently reproduced A's state for %d blocks (no divergence)", len(blocks))
}

// TestApplyGate_HaltOnDivergence: a store whose state is perturbed after genesis
// must HALT when applying a block whose fingerprint was stamped from clean state.
func TestApplyGate_HaltOnDivergence(t *testing.T) {
	code := deployBytecode(t)

	// Clean run on A → stamped blocks.
	blocks := produceOnA(t, t.TempDir(), code)
	deploy, call := blocks[0], blocks[1]

	// Perturbed store: apply deploy, corrupt a balance, then apply the clean-stamped
	// call block → expect a divergence halt.
	cleanup := buildHandle(t, t.TempDir())
	defer cleanup()
	seedGenesis(t)
	if err := BlockProcessing.ProcessBlockTransactions(context.Background(), deploy, nil); err != nil {
		t.Fatalf("apply deploy: %v", err)
	}
	// Bump acctB's balance so the recomputed fingerprint cannot match A's stamp.
	if err := DB_OPs.UpdateAccountBalance(nil, acctB, new(big.Int).Add(oneK, big.NewInt(1)).String(), 0); err != nil {
		t.Fatalf("perturb balance: %v", err)
	}
	err := BlockProcessing.ProcessBlockTransactions(context.Background(), call, nil)
	if err == nil {
		t.Fatal("expected halt on state divergence, but apply succeeded (P2.5 did not fire)")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "divergence") {
		t.Fatalf("expected a state-divergence halt, got: %v", err)
	}
	t.Logf("PASS: perturbed store halted as expected: %v", err)
}
