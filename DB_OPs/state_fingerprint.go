// MODULE: DB_OPs/state_fingerprint
// PURPOSE: Deterministic digest of the full account state so operators can
//          compare nodes directly: two nodes at the same block height with
//          the same fingerprint hold identical balances/nonces; a differing
//          fingerprint pinpoints state disagreement even when block-hash
//          Merkle roots (which cover chain data, not balances) match.
//
// The digest streams accounts in ascending key order (the pagination order of
// ListAccountsPaginatedFrom, identical on every node) and hashes the fields
// consensus must agree on: address, balance, tx nonce, sent-tx count. Volatile
// local metadata (UpdatedAt, CreatedAt, DID linkage, custom metadata) is
// deliberately excluded — it may legitimately differ across nodes.

package DB_OPs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"gossipnode/consensushash"
)

// ComputeAccountStateFingerprintV1 is the consensus (audit P2.5) post-apply
// account-state fingerprint: the canonical, domain-tagged keccak digest
// (consensushash.StateFingerprintV1) over every account's consensus-relevant
// fields, streamed in the deterministic ListAccountsPaginatedFrom key order so
// every node at the same height with the same ledger computes the identical hex
// digest. A mismatch between a node's recompute and the block-carried value means
// the ledgers diverged.
//
// v1 covers PLAIN ACCOUNTS only (the reproduced live-vs-synced divergence is an
// account-balance divergence); contract state is committed separately by the
// state root in P4. O(N) over all accounts per call — gate its use (it runs only
// when contract execution is enabled) and make it incremental if N grows large.
func ComputeAccountStateFingerprintV1(ctx context.Context) (string, error) {
	const pageSize = 1000
	f := consensushash.NewStateFingerprinterV1()
	var lastKey []byte
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		accs, nextKey, err := ListAccountsPaginatedFrom(nil, pageSize, lastKey, "")
		if err != nil {
			return "", fmt.Errorf("state fingerprint v1: page after %q: %w", string(lastKey), err)
		}
		if len(accs) == 0 {
			break
		}
		for _, acc := range accs {
			if acc == nil {
				continue
			}
			f.FoldAccount(consensushash.AccountLeaf{
				Address:     acc.Address.Hex(),
				Balance:     acc.Balance,
				TxNonce:     acc.TxNonce,
				TxCountSent: acc.TxCountSent,
			})
		}
		if nextKey == nil || len(accs) < pageSize {
			break
		}
		lastKey = nextKey
	}
	return f.Sum().Hex(), nil
}

// ComputeAccountStateFingerprint hashes every account's consensus-relevant
// fields in ascending key order. Returns the hex digest and the number of
// accounts included.
//
// O(N) over all accounts in pages of pageSize; memory is O(pageSize).
func ComputeAccountStateFingerprint(ctx context.Context) (string, uint64, error) {
	const pageSize = 1000

	h := sha256.New()
	var count uint64
	var lastKey []byte

	for {
		select {
		case <-ctx.Done():
			return "", count, ctx.Err()
		default:
		}

		accs, nextKey, err := ListAccountsPaginatedFrom(nil, pageSize, lastKey, "")
		if err != nil {
			return "", count, fmt.Errorf("state fingerprint: page after %q: %w", string(lastKey), err)
		}
		if len(accs) == 0 {
			break
		}
		for _, acc := range accs {
			if acc == nil {
				continue
			}
			balance := acc.Balance
			if balance == "" {
				balance = "0"
			}
			// Lowercased address keeps the digest independent of checksum
			// casing differences between write paths.
			fmt.Fprintf(h, "%s|%s|%d|%d\n",
				strings.ToLower(acc.Address.Hex()), balance, acc.TxNonce, acc.TxCountSent)
			count++
		}
		if nextKey == nil || len(accs) < pageSize {
			break
		}
		lastKey = nextKey
	}

	return hex.EncodeToString(h.Sum(nil)), count, nil
}
