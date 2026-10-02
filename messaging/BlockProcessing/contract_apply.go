package BlockProcessing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs"
	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/execbridge"
)

// contractBlockGasLimit is the block gas limit exposed to the EVM (GASLIMIT
// opcode) during contract execution. Matches the SmartContract EVM default.
const contractBlockGasLimit = uint64(30_000_000)

// acctNotFound mirrors the not-found check used elsewhere in this package
// (processTransaction snapshot loop): a missing account is a fresh account, not
// a hard error.
func acctNotFound(err error) bool {
	// Canonical matcher: KV ("key not found") AND SQL ("no rows in result set").
	// A first-time contract address read from the SQL-backed store surfaces the
	// latter, which the old narrow check missed → the deploy was rejected.
	return err != nil && DB_OPs.IsNotFound(err)
}

// ErrUnstampedNewAccount marks a contract tx that was applied as REVERTED
// because its execution created an account with no block-carried ART identity
// (post-activation backstop; see unstampedAccountRevertActive).
var ErrUnstampedNewAccount = errors.New("contract execution created an account with no block-carried ART identity")

// unstampedAccountRevertActive reports whether the deterministic backstop applies
// at blockNumber: a contract tx whose execution creates an unstamped account is
// applied as an EVM revert instead of failing the whole block. A FLEET-AGREED
// consensus parameter (consensus.evm_unstamped_account_revert_height): 0 = off
// (legacy whole-block failure, unchanged).
func unstampedAccountRevertActive(blockNumber uint64) bool {
	if !settings.IsLoaded() {
		return false
	}
	h := settings.Get().Consensus.EVMUnstampedAccountRevertHeight
	return h != 0 && blockNumber >= h
}

// contractExecContext is the ONE constructor of the EVM block environment for a
// contract tx. Shared by the apply path and the sequencer's pre-consensus
// prediction so both execute against an identical context.
func contractExecContext(blockNumber uint64, blockHash common.Hash, blockTimestamp int64, coinbase common.Address, txIndex int) execbridge.BlockExecContext {
	return execbridge.BlockExecContext{
		ChainID:     settings.Get().Network.ChainID,
		BlockNumber: blockNumber,
		BlockHash:   blockHash,
		Time:        blockTimestamp,
		Coinbase:    coinbase,
		TxIndex:     txIndex,
		GasLimit:    contractBlockGasLimit,
	}
}

// sortedAddrs returns m's keys in ascending byte order (deterministic iteration).
func sortedAddrs(m map[common.Address]*big.Int) []common.Address {
	out := make([]common.Address, 0, len(m))
	for a := range m {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}

// loadContractTouched stages every touched account and returns its pre-balance.
// An account absent from the store is created from its block-carried ART identity;
// one with no carried identity is NOT created and is returned in unstamped
// (sorted, deterministic) for the caller to decide. A non-not-found read error or
// an unparseable balance is returned as err (fail closed).
func loadContractTouched(stage *txStage, touched []common.Address, accountNonces map[common.Address]uint64, isDeploy bool, deployed common.Address, ts int64) (*txStage, map[common.Address]*big.Int, []common.Address, error) {
	pre := make(map[common.Address]*big.Int)
	var unstamped []common.Address
	seen := make(map[common.Address]bool)
	for _, a := range touched {
		if seen[a] {
			continue
		}
		seen[a] = true
		doc, gerr := stage.get(a)
		if gerr != nil || doc == nil {
			if gerr != nil && !acctNotFound(gerr) {
				return nil, nil, nil, fmt.Errorf("load account %s: %w", a.Hex(), gerr)
			}
			// New account's ART identity is the block-carried monotonic ordinal the
			// sequencer stamped (EnrichBlockAccountNoncesWithPredicted, including the
			// deployed contract and execution-created accounts). No local mint.
			artNonce, ok := accountNonces[a]
			if !ok || artNonce == 0 {
				unstamped = append(unstamped, a)
				continue
			}
			accType := "user"
			if isDeploy && a == deployed {
				accType = "contract"
			}
			doc = &DB_OPs.Account{
				Nonce:       artNonce,
				DIDAddress:  "did:jmdt:metamask:" + a.Hex(),
				Address:     a,
				Balance:     "0",
				AccountType: accType,
				CreatedAt:   ts,
				UpdatedAt:   ts,
			}
			stage.put(doc)
		}
		b := new(big.Int)
		if doc.Balance != "" {
			if _, ok := b.SetString(doc.Balance, 10); !ok {
				return nil, nil, nil, fmt.Errorf("bad balance %q for %s", doc.Balance, a.Hex())
			}
		}
		pre[a] = b
	}
	sort.Slice(unstamped, func(i, j int) bool { return bytes.Compare(unstamped[i][:], unstamped[j][:]) < 0 })
	return stage, pre, unstamped, nil
}

// applyContractTx applies a contract transaction (deployment, or a call to an
// address holding code) on the consensus apply path — audit EVM-01 wiring, P2.
//
// It executes deterministically via the execbridge executor (local-ledger state,
// block-derived context — EVM-A16/EVM-29/EVM-30), then folds the resulting native
// value movements + the protocol gas fee through config.FoldContractExecution
// (the ONE fee formula, with conservation + solvency fail-closed guards), and
// commits the absolute account docs together with the tx_processed marker via
// DB_OPs.ApplyTxAtomic — the SAME atomic primitive the value-transfer path uses.
//
// Preconditions: the caller (processTransaction) holds DB_OPs.LockStateApply, has
// confirmed the tx is not already processed, and has set the processing marker.
// Any inconsistency returns an error so the WHOLE block fails (determinism); a
// reverted EVM execution is NOT an error — it still charges gas and commits.
//
// Newly-created accounts (the deployed contract, or a value recipient not seen
// before) take their FastSync ART identity ONLY from the block-carried nonce map
// (a monotonic ordinal the sequencer stamps in DB_OPs.EnrichBlockAccountNonces,
// which now also stamps the CREATE-deterministic deployed-contract address); there
// is no local mint, and the apply path fails closed if the identity is absent.
func applyContractTx(
	span_ctx context.Context,
	tx config.Transaction,
	coinbaseAddr, zkvmAddr common.Address,
	feeRecipients []config.FeeRecipient,
	accountsClient *config.PooledConnection,
	blockNumber uint64,
	blockHash common.Hash,
	txIndex int,
	blockTimestamp int64,
	accountNonces map[common.Address]uint64,
) error {
	sender := *tx.From
	ts := blockTimestamp * int64(time.Second)
	fail := func(format string, args ...interface{}) error {
		cleanupProcessingMarkers(span_ctx, accountsClient, tx.Hash.String())
		return fmt.Errorf(format, args...)
	}

	// 1. Execute deterministically through the seam. The context comes from the
	//    SAME helper the sequencer's pre-consensus prediction uses
	//    (PredictContractCreatedAccounts), so both see an identical EVM block env.
	res, err := execbridge.Get().ExecuteTx(span_ctx, &tx, contractExecContext(blockNumber, blockHash, blockTimestamp, coinbaseAddr, txIndex))
	if err != nil {
		return fail("contract tx %s execution error: %w", tx.Hash.Hex(), err)
	}
	if res == nil || !res.Handled {
		return fail("contract executor declined tx %s (IsContractTx/ExecuteTx disagree)", tx.Hash.Hex())
	}

	gasFee := config.GasFee(tx.Type, tx.GasLimit, tx.GasPrice, tx.MaxFee, tx.MaxPriorityFee)
	isDeploy := tx.To == nil

	// Native value movements (absolute) — only on success; a reverted tx moves no
	// value and pays gas only.
	evmAbs := make(map[common.Address]*big.Int)
	if res.Success {
		for a, v := range res.BalanceChanges {
			if v != nil {
				evmAbs[a] = new(big.Int).Set(v)
			}
		}
	}

	// 2. Assemble the touched accounts: sender, zkvm, coinbase, fee recipients
	//    (the fee-path base set), then the EXECUTION-touched accounts: every
	//    value-touched address (internal CALL{value}, SELFDESTRUCT beneficiary,
	//    CREATE/CREATE2 child funded with value) and, on a successful deployment,
	//    the new contract.
	base := []common.Address{sender, zkvmAddr, coinbaseAddr}
	for _, r := range feeRecipients {
		base = append(base, r.Addr)
	}
	touched := append([]common.Address{}, base...)
	touched = append(touched, sortedAddrs(evmAbs)...)
	if res.Success && isDeploy && res.ContractAddress != (common.Address{}) {
		touched = append(touched, res.ContractAddress)
	}

	// 3. Load pre-balances (staging docs), creating new accounts from the
	//    block-carried identity (no local mint).
	stage, pre, unstamped, lerr := loadContractTouched(newTxStage(accountsClient), touched, accountNonces, isDeploy, res.ContractAddress, ts)
	if lerr != nil {
		return fail("contract tx %s: %w", tx.Hash.Hex(), lerr)
	}
	if len(unstamped) > 0 {
		// A new account created DURING execution carries no block-carried ART
		// identity. The sequencer's pre-consensus prediction
		// (PredictContractCreatedAccounts) stamps every account a contract tx creates
		// when simulated against the pre-block state, so this is reached only when
		// the prediction could not see it (e.g. the tx depends on an earlier tx in
		// the SAME block) or the block came from a pre-upgrade sequencer.
		if !unstampedAccountRevertActive(blockNumber) {
			// Legacy rule (pre-activation): fail the whole block, unchanged.
			return fail("contract tx %s: new account %s has no block-carried ART identity", tx.Hash.Hex(), unstamped[0].Hex())
		}
		// Deterministic backstop (post-activation): treat the tx exactly like an EVM
		// revert — discard its execution effects (value moves, created accounts,
		// contract storage), charge gas, bump the sender nonce, status-0 receipt.
		// Every node holds the SAME carried AccountNonces and computes the SAME EVM
		// result, so every node takes this branch identically; the block applies.
		logger().Warn(span_ctx, "Contract tx created an account with no block-carried ART identity — applying as REVERTED (deterministic backstop)",
			ion.String("tx_hash", tx.Hash.Hex()),
			ion.String("account", unstamped[0].Hex()),
			ion.Int("unstamped_accounts", len(unstamped)),
			ion.Uint64("block_number", blockNumber),
			ion.String("function", "BlockProcessing.applyContractTx"))
		res = &execbridge.ExecResult{
			Handled: true,
			Success: false,
			GasUsed: res.GasUsed,
			Err:     fmt.Errorf("%w: %s", ErrUnstampedNewAccount, unstamped[0].Hex()),
		}
		evmAbs = map[common.Address]*big.Int{}
		stage, pre, unstamped, lerr = loadContractTouched(newTxStage(accountsClient), base, accountNonces, isDeploy, common.Address{}, ts)
		if lerr != nil {
			return fail("contract tx %s: %w", tx.Hash.Hex(), lerr)
		}
		if len(unstamped) > 0 {
			// A fee-path account (sender/coinbase/zkvm/fee recipient) is new and
			// unstamped — enrichment always stamps these, so this is a malformed
			// block. Fail closed as before.
			return fail("contract tx %s: new account %s has no block-carried ART identity", tx.Hash.Hex(), unstamped[0].Hex())
		}
	}

	// 4. Stale-nonce guard (mirror deductFromSender): fail the block if the
	//    sender's account nonce already moved past this tx.
	if senderDoc, _ := stage.get(sender); senderDoc != nil && tx.Nonce < senderDoc.TxNonce {
		return fail("%w: contract tx nonce %d < account nonce %d", ErrStaleNonce, tx.Nonce, senderDoc.TxNonce)
	}

	// 5. Fold native value + gas into final absolute balances (fail-closed on
	//    non-conservation or insolvency).
	final, ferr := config.FoldContractExecution(pre, evmAbs, sender, zkvmAddr, coinbaseAddr, gasFee, feeRecipients)
	if ferr != nil {
		return fail("contract tx %s fold: %w", tx.Hash.Hex(), ferr)
	}

	// 6. Write final balances onto the staged docs.
	for a, bal := range final {
		doc, derr := stage.get(a)
		if derr != nil || doc == nil {
			return fail("contract tx %s: staged account %s vanished before commit", tx.Hash.Hex(), a.Hex())
		}
		doc.Balance = bal.String()
		doc.UpdatedAt = ts
		stage.put(doc)
	}

	// 7. Sender nonce + sent-count bump for the committed tx (mirror
	//    deductFromSender), then identity-heal from the block-carried nonce.
	if senderDoc, _ := stage.get(sender); senderDoc != nil {
		senderDoc.TxNonce = tx.Nonce + 1
		senderDoc.TxCountSent = senderDoc.TxCountSent + 1
		senderDoc.UpdatedAt = ts
		stage.put(senderDoc)
	}
	adoptCarriedNonce(span_ctx, stage, &sender, accountNonces)

	// 7.5 Commit contract state AFTER the fold + all deterministic checks (fold
	//     conservation/solvency, block-carried ART identity, stale nonce) have
	//     passed, but BEFORE the account atomic apply (NEW-2: commit-after-fold).
	//     The executor deferred this commit; running it here means a rejected block
	//     never leaves orphaned contract writes (the old order committed inside
	//     ExecuteTx, so any later failure orphaned a rejected block's contract
	//     state). A commit I/O failure is non-deterministic → fail the block; no
	//     account state has been committed yet, so nothing is left inconsistent.
	if res.Success && res.CommitState != nil {
		if _, cErr := res.CommitState(); cErr != nil {
			return fail("contract tx %s: commit contract state: %w", tx.Hash.Hex(), cErr)
		}
	}
	// D-858 review B2 + B2-a: flag the block whenever a contract tx SUCCEEDED, not
	// only when CommitState ran. A successful tx credits res.BalanceChanges (arbitrary
	// third parties) and, on deploy, res.ContractAddress (above) — both gated on
	// res.Success ALONE and both OUTSIDE affectedAccountsForBlock. So the store-failure
	// rollback is incomplete for ANY successful contract tx, even one that wrote no
	// storage (nil CommitState). Gating the flag on res.Success (not CommitState != nil)
	// closes the "Success with nil CommitState" hole that would otherwise let the
	// original B2 defect return silently. A reverted tx (res.Success == false) moves no
	// value and pays gas only — fully covered by affectedAccountsForBlock — so no flag.
	if res.Success {
		markContractStateCommitted(blockHash)
	}

	// 8. Commit: accounts + tx_processed marker via the atomic primitive.
	if err := DB_OPs.ApplyTxAtomic(accountsClient, stage.staged(), tx.Hash.String(), time.Now().UTC().Unix()); err != nil {
		return fail("contract tx %s atomic commit failed: %w", tx.Hash.Hex(), err)
	}
	cleanupProcessingMarkers(span_ctx, accountsClient, tx.Hash.String())

	// 9. Persist the contract receipt through the gateway 2PC path (→ SQL
	//    contract_receipts) — the same synchronous path WriteTransaction uses, so it
	//    works without a projector Runner. Best-effort: a derived-index write must
	//    not fail an already-committed block; eth_getTransactionReceipt falls back to
	//    reconstruction if the row is absent.
	{
		status := int16(0)
		if res.Success {
			status = 1
		}
		var caddr *string
		if res.Success && isDeploy && res.ContractAddress != (common.Address{}) {
			s := res.ContractAddress.Hex()
			caddr = &s
		}
		var logsJSON []byte
		if res.Success && len(res.Logs) > 0 {
			// Stamp block/tx context onto the raw EVM logs. The StateDB only fills
			// address/topics/data; without this every receipt log carried zeroed
			// blockHash / transactionHash / logIndex and clients could not key
			// events. logIndex is BLOCK-wide (geth semantics), so it continues
			// from the previous contract tx in the same block.
			first := nextLogIndex(blockHash, uint(len(res.Logs)))
			for i, l := range res.Logs {
				if l == nil {
					continue
				}
				l.BlockNumber = blockNumber
				l.BlockHash = blockHash
				l.TxHash = tx.Hash
				l.TxIndex = uint(txIndex)
				l.Index = first + uint(i)
			}
			if b, mErr := json.Marshal(res.Logs); mErr == nil {
				logsJSON = b
			}
			// Index the logs for eth_getLogs (KV log store) and fan out to
			// eth_subscribe("logs") listeners. Best-effort like the receipt row:
			// the block is already committed, a derived-index failure must not
			// fail it — eth_getTransactionReceipt still serves the logs.
			if wErr := DB_OPs.GlobalLogWriter.Write(res.Logs); wErr != nil {
				logger().Warn(span_ctx, "persist event logs failed",
					ion.String("tx", tx.Hash.Hex()), ion.String("err", wErr.Error()))
			}
		}
		revertReason := ""
		if !res.Success && res.Err != nil {
			revertReason = res.Err.Error()
		}
		rec := &thebegateway.ContractReceiptRecord{
			TxHash:          tx.Hash.Hex(),
			BlockNumber:     blockNumber,
			TxIndex:         int16(txIndex),
			Status:          status,
			GasUsed:         strconv.FormatUint(res.GasUsed, 10),
			ContractAddress: caddr,
			Logs:            logsJSON,
			RevertReason:    revertReason,
			CreatedAt:       time.Unix(blockTimestamp, 0).UTC(),
		}
		if rErr := DB_OPs.WriteContractReceipt(accountsClient, rec); rErr != nil {
			// The block row is stored only AFTER all txs are applied (broadcast.go:
			// "process transactions BEFORE storing the block"), so this write's
			// FK on blocks fails on the first attempt by construction and lands
			// via the outbox once StoreZKBlock has run. That is expected; only an
			// error that was NOT enqueued is worth a warning.
			if strings.Contains(rErr.Error(), "enqueued to outbox") {
				logger().Info(span_ctx, "contract receipt deferred to outbox (block row not yet stored)",
					ion.String("tx", tx.Hash.Hex()))
			} else {
				logger().Warn(span_ctx, "persist contract receipt failed",
					ion.String("tx", tx.Hash.Hex()), ion.String("err", rErr.Error()))
			}
		}
	}

	logger().Info(span_ctx, "Contract transaction applied",
		ion.String("tx_hash", tx.Hash.Hex()),
		ion.Bool("deploy", isDeploy),
		ion.Bool("success", res.Success),
		ion.String("contract", res.ContractAddress.Hex()),
		ion.Uint64("gas_used", res.GasUsed),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("topic", TOPIC),
		ion.String("function", "BlockProcessing.applyContractTx"),
	)
	return nil
}

// ── block-wide log index ─────────────────────────────────────────────────────
//
// eth_getTransactionReceipt.logs[].logIndex and eth_getLogs are defined as the
// log's position within the BLOCK, not the tx. Block application is sequential
// (under LockStateApply) and txs are applied in block order, so a single
// counter keyed by block hash is enough: it resets when a new block starts.

var logIdx struct {
	mu    sync.Mutex
	block common.Hash
	next  uint
}

// nextLogIndex reserves n consecutive block-wide log indices for a tx in
// blockHash and returns the first one.
func nextLogIndex(blockHash common.Hash, n uint) uint {
	logIdx.mu.Lock()
	defer logIdx.mu.Unlock()
	if logIdx.block != blockHash {
		logIdx.block = blockHash
		logIdx.next = 0
	}
	first := logIdx.next
	logIdx.next += n
	return first
}
