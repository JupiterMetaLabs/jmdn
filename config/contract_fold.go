// MODULE: config/contract_fold
// PURPOSE: Single source of truth for folding a contract transaction's effects
// into authoritative ledger balances (EVM integration P2, "persist now").
//
// The deterministic EVM runs at gas price 0 (gas is charged by the protocol, not
// inside the VM), so the executor's absolute post-execution balances reflect ONLY
// native-value movements (msg.value transfers, internal .transfer/.call{value},
// selfdestruct, creation-with-value) — never gas. This function takes those
// absolute value balances and applies the protocol gas fee on top, using the
// SAME config.GasFee/SplitFee formula as the plain-transfer path, so the two can
// never disagree on a wei (the divergence class this whole module guards against).
//
// It is a PURE function of its inputs and fails closed on any violation:
//   - native-coin conservation: the EVM must not mint or burn native coin, so the
//     value deltas must sum to zero;
//   - solvency: no account may end negative (sender must afford value + gas).
package config

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// FoldContractExecution reconciles a contract tx's EVM value movements and its
// protocol gas fee into final ABSOLUTE ledger balances, deterministically.
//
//	pre       — authoritative pre-tx ledger balances for EVERY account that will
//	            be touched (sender, value recipients, zkvm, coinbase/recipients).
//	            A missing account is treated as balance 0.
//	evmAbs    — the executor's absolute post-execution balances (GetBalanceChanges),
//	            seeded from the same ledger, reflecting value moves only (no gas).
//	sender    — the tx sender, charged the gas fee.
//	zkvmAddr  — recipient of the ZKVM fee share (floor(gasFee/2)).
//	coinbase  — coinbase for the coinbase-side share when recipients is empty.
//	gasFee    — total gas fee (config.GasFee); nil == 0.
//	recipients— optional weighted fee recipients (SplitFee distributes to them).
//
// Returns the final absolute balances for exactly the touched accounts.
// Fail-closed errors: value not conserved (mint/burn), or an ending negative
// balance (insolvent sender / underflow).
func FoldContractExecution(
	pre map[common.Address]*big.Int,
	evmAbs map[common.Address]*big.Int,
	sender, zkvmAddr, coinbase common.Address,
	gasFee *big.Int,
	recipients []FeeRecipient,
) (map[common.Address]*big.Int, error) {
	get := func(m map[common.Address]*big.Int, a common.Address) *big.Int {
		if v, ok := m[a]; ok && v != nil {
			return v
		}
		return new(big.Int)
	}

	// 1. Value deltas from the EVM (evmAbs - pre) must NET TO ZERO: the EVM only
	//    MOVES native coin between accounts, never creates or destroys it. A
	//    non-zero sum means the executor minted/burned coin — refuse (this is the
	//    exact silent-divergence class the audit flagged, PRE-4).
	final := make(map[common.Address]*big.Int, len(evmAbs)+len(recipients)+2)
	valueSum := new(big.Int)
	for addr, abs := range evmAbs {
		if abs == nil {
			abs = new(big.Int)
		}
		delta := new(big.Int).Sub(abs, get(pre, addr))
		valueSum.Add(valueSum, delta)
		final[addr] = new(big.Int).Set(abs) // start from the EVM's post-value balance
	}
	if valueSum.Sign() != 0 {
		return nil, fmt.Errorf("contract fold: native value not conserved (delta sum=%s); EVM must not mint or burn coin", valueSum)
	}

	// ensure returns the working absolute balance for addr, seeding from evmAbs
	// (already in final) else from pre.
	ensure := func(addr common.Address) *big.Int {
		if v, ok := final[addr]; ok {
			return v
		}
		v := new(big.Int).Set(get(pre, addr))
		final[addr] = v
		return v
	}

	// 2. Charge the gas fee to the sender and distribute it via the ONE fee
	//    formula. SplitFee guarantees zkvmShare + Σcredits == gasFee, so the gas
	//    leg also nets to zero — total conservation holds.
	fee := gasFee
	if fee == nil {
		fee = new(big.Int)
	}
	if fee.Sign() > 0 {
		ensure(sender).Sub(ensure(sender), fee)

		zkvmShare, credits := SplitFee(fee, coinbase, recipients)
		ensure(zkvmAddr).Add(ensure(zkvmAddr), zkvmShare)
		for _, c := range credits {
			ensure(c.Addr).Add(ensure(c.Addr), c.Amount)
		}
	}

	// 3. Solvency: no account may end negative (sender could not afford value+gas,
	//    or an underflow slipped through). Fail closed.
	for addr, bal := range final {
		if bal.Sign() < 0 {
			return nil, fmt.Errorf("contract fold: account %s would end with a negative balance %s (insufficient funds for value+gas)", addr.Hex(), bal)
		}
	}
	return final, nil
}
