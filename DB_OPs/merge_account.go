// MODULE: DB_OPs/merge_account.go
// PURPOSE: The PURE account-merge decision point (F1–F6 train). Extracted
//          byte-identical from account_immuclient.go when the ImmuDB transport
//          around it was removed in the ThebeDB migration. Every account write
//          path (BatchRestoreAccounts → drain worker, restores, sync payloads)
//          MUST route through mergeAccountForWrite.
//
// INVARIANTS (pinned by merge_account_test.go — do not edit without the RCA):
//   - LWW on unit-NORMALIZED timestamps (stored data is mixed seconds/nanos)
//   - identity-field preservation (DID, CreatedAt, AccountType, Metadata)
//   - monotonic TxNonce / TxCountSent
//   - ART identity nonce preserved when incoming is zero

package DB_OPs

import (
	"strings"
	"time"
)

// normalizeUpdatedAtNanos converts an UpdatedAt value of unknown unit
// (seconds, millis, micros, or nanos since epoch) to nanoseconds so LWW
// comparisons are unit-safe. Needed because the live executor stamps
// UpdatedAt with the block timestamp (Unix seconds) while sync paths stamp
// time.Now().UnixNano() — comparing them raw makes any nano-stamped write
// beat every second-stamped write by 9 orders of magnitude.
func normalizeUpdatedAtNanos(ts int64) int64 {
	switch {
	case ts <= 0:
		return ts
	case ts < 1e11: // seconds (valid until year ~5138)
		return ts * int64(time.Second)
	case ts < 1e14: // milliseconds
		return ts * int64(time.Millisecond)
	case ts < 1e17: // microseconds
		return ts * int64(time.Microsecond)
	default: // already nanoseconds
		return ts
	}
}

// mergeAccountForWrite is the single, PURE decision point for writing an
// account object over stored state. It owns LWW ordering, identity-field
// preservation, monotonic counter guards, and new-account defaults — every
// write path through BatchRestoreAccounts (sync accounts payloads, sparse
// balance updates, restores) goes through this function. Unit-tested in
// merge_account_test.go; keep it free of I/O and logging.
//
// existing == nil means no stored account (new account). Returns the merged
// object and whether it should be written (false = existing state wins LWW).
func mergeAccountForWrite(existing *Account, incoming Account) (Account, bool) {
	if existing == nil {
		// NEW ACCOUNT (no stored object to merge from): fill defaults for
		// identity fields that sparse update entries leave zero-valued.
		// DIDAddress stays empty — hex addresses are not DIDs; the real DID
		// arrives later via the accounts payload or DID propagation.
		if incoming.AccountType == "" {
			incoming.AccountType = "user"
		}
		if incoming.CreatedAt == 0 && incoming.UpdatedAt != 0 {
			incoming.CreatedAt = incoming.UpdatedAt
		}
		return incoming, true
	}

	// LWW on unit-normalized timestamps — stored values may be in seconds
	// (live executor: block timestamp) or nanos (sync paths).
	existingTS := normalizeUpdatedAtNanos(existing.UpdatedAt)
	incomingTS := normalizeUpdatedAtNanos(incoming.UpdatedAt)
	if existingTS > incomingTS {
		return incoming, false
	}
	if existingTS == incomingTS && existing.Balance == incoming.Balance {
		// Same timestamp and balance - no change needed
		return incoming, false
	}

	// FIELD MERGING: Prevent partial updates (e.g. from Reconciliation) from wiping out account metadata
	// 1. Preserve DIDAddress if incoming DID is empty or mistakenly set to the
	// hex address. EqualFold: legacy update entries carried the address in
	// lowercase while Address.Hex() is EIP-55 checksummed — a case-sensitive
	// compare never matched, letting the forged DID overwrite the real one.
	if incoming.DIDAddress == "" || strings.EqualFold(incoming.DIDAddress, incoming.Address.Hex()) {
		incoming.DIDAddress = existing.DIDAddress
	}
	// 2. Preserve CreatedAt
	if incoming.CreatedAt == 0 {
		incoming.CreatedAt = existing.CreatedAt
	}
	// 3. Preserve AccountType. Empty = balance update carries no identity;
	// "user" = legacy hardcoded placeholder from old update entries.
	if (incoming.AccountType == "" || incoming.AccountType == "user") && existing.AccountType != "" {
		incoming.AccountType = existing.AccountType
	}
	// 4. Preserve Metadata
	if incoming.Metadata == nil {
		incoming.Metadata = existing.Metadata
	}
	// 5. Preserve ART identity nonce: 0 means the producer had no value
	// (e.g. reconciliation of a receiver-only account). Never zero it.
	if incoming.Nonce == 0 {
		incoming.Nonce = existing.Nonce
	}
	// 6. Monotonic guard on tx counters: the Ethereum nonce and sent-tx
	// count never decrease. A lower incoming value means the producer had
	// partial information (receiver-only recon delta) — keep the existing.
	if incoming.TxNonce < existing.TxNonce {
		incoming.TxNonce = existing.TxNonce
	}
	if incoming.TxCountSent < existing.TxCountSent {
		incoming.TxCountSent = existing.TxCountSent
	}
	// 7. Preserve Balance on a placeholder/sync write. The authoritative balance
	// writers — live execution (ApplyTxAtomic) and reconciliation
	// (ApplyBlockRecon) — commit directly under the state-apply lock and never
	// reach this merge. The writes that DO reach it are account-sync, restore,
	// and DID propagation, which carry Balance "0" as a placeholder that
	// reconciliation is expected to fill. Letting that "0" win LWW would
	// overwrite a real balance with zero — a silent, non-healing divergence.
	// Treat an incoming zero/empty balance as "no balance information" and keep
	// the stored value, exactly like the sparse-field preserves above. A real
	// (nonzero) incoming balance is still applied, so legitimate balance updates
	// that route through this path are unaffected.
	if isZeroBalanceString(incoming.Balance) && !isZeroBalanceString(existing.Balance) {
		incoming.Balance = existing.Balance
	}

	return incoming, true
}

// isZeroBalanceString reports whether bal carries no balance information
// ("" or "0") — the placeholder shape produced by account-sync, restore and
// DID-propagation writers.
func isZeroBalanceString(bal string) bool {
	return bal == "" || bal == "0"
}
