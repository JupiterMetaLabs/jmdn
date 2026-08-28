package BlockProcessing

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
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

	// 1. Execute deterministically through the seam.
	res, err := execbridge.Get().ExecuteTx(span_ctx, &tx, execbridge.BlockExecContext{
		ChainID:     settings.Get().Network.ChainID,
		BlockNumber: blockNumber,
		BlockHash:   blockHash,
		Time:        blockTimestamp,
		Coinbase:    coinbaseAddr,
		TxIndex:     txIndex,
		GasLimit:    contractBlockGasLimit,
	})
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

	// 2. Assemble the touched accounts: sender, zkvm, coinbase, fee recipients,
	//    value-touched accounts, and (successful deployment) the new contract.
	touched := []common.Address{sender, zkvmAddr, coinbaseAddr}
	for _, r := range feeRecipients {
		touched = append(touched, r.Addr)
	}
	for a := range evmAbs {
		touched = append(touched, a)
	}
	if res.Success && isDeploy && res.ContractAddress != (common.Address{}) {
		touched = append(touched, res.ContractAddress)
	}

	// 3. Load pre-balances (staging docs), creating new accounts from the
	//    block-carried identity (fail-closed if absent).
	stage := newTxStage(accountsClient)
	pre := make(map[common.Address]*big.Int)
	for _, a := range touched {
		if _, ok := pre[a]; ok {
			continue
		}
		doc, gerr := stage.get(a)
		if gerr != nil || doc == nil {
			if gerr != nil && !acctNotFound(gerr) {
				return fail("contract tx %s: load account %s: %w", tx.Hash.Hex(), a.Hex(), gerr)
			}
			// New account's ART identity is the block-carried monotonic ordinal the
			// sequencer stamped (EnrichBlockAccountNonces, including the deployed
			// contract address). No local mint — fail closed if absent.
			artNonce, ok := accountNonces[a]
			if !ok || artNonce == 0 {
				return fail("contract tx %s: new account %s has no block-carried ART identity", tx.Hash.Hex(), a.Hex())
			}
			accType := "user"
			if isDeploy && a == res.ContractAddress {
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
				return fail("contract tx %s: bad balance %q for %s", tx.Hash.Hex(), doc.Balance, a.Hex())
			}
		}
		pre[a] = b
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
			if b, mErr := json.Marshal(res.Logs); mErr == nil {
				logsJSON = b
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
			logger().Warn(span_ctx, "persist contract receipt failed",
				ion.String("tx", tx.Hash.Hex()), ion.String("err", rErr.Error()))
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
