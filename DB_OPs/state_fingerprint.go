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
)

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
