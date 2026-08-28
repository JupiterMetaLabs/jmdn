package DB_OPs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// BackfillAccountNonces retro-stamps ZKBlock.AccountNonces into the SQL
// projection's blocks.extra_data for every block that lacks it.
//
// WHY: ThebeSync serves blocks reconstructed from the SQL projection
// (Provider.RawBlock -> GetZKBlockByNumber). AccountNonces — the canonical ART
// identity per touched account, stamped by EnrichBlockAccountNonces at ingest —
// is ADVISORY (not part of the canonical block hash). Blocks written before that
// field was persisted carry it in neither the KV log nor SQL, so a catch-up node
// can't create new accounts (contract deploys, first-time receivers) and apply
// fails with "no block-carried ART identity".
//
// Re-deriving on the receiver is UNSAFE: the ART ordinal counter advances for
// rejected/reorged proposals, so a node that saw only accepted blocks cannot
// reproduce the sequence. This backfill instead reads each touched account's
// canonical, IMMUTABLE identity (Account.Nonce) from the accounts store — which
// already reflects the true assignment, gaps included — and writes it back onto
// the block, so the served block carries the exact identity apply expects.
//
// RUN ON THE SEQUENCER (the canonical source that serves catch-up). The sequencer
// stamps identities and never adopt-heals its own values, so its accounts store
// holds exactly what each block originally carried. Running it on a validator
// that has adopted values could stamp a healed (later) identity.
//
// SQL-projection only: the advisory field is not in the KV canonical log, so a
// full projection rebuild-from-KV drops it — re-run after any such rebuild. New
// blocks carry it durably via the write path (backend/block.go).
//
// Idempotent (a block whose extra_data already has account_nonces is skipped) and
// fail-closed (a touched account missing from the store aborts, since an applied
// chain must contain every touched account).
func BackfillAccountNonces(ctx context.Context, sqlDB *sql.DB) (updated, skipped int, err error) {
	if sqlDB == nil {
		return 0, 0, fmt.Errorf("BackfillAccountNonces: nil sqlDB")
	}
	tip, err := GetLatestBlockNumber(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("BackfillAccountNonces: latest block: %w", err)
	}

	for n := uint64(0); n <= tip; n++ {
		// Idempotent skip — key already present.
		var present bool
		if qerr := sqlDB.QueryRowContext(ctx,
			`SELECT (extra_data->'account_nonces') IS NOT NULL FROM blocks WHERE block_number = $1`, n,
		).Scan(&present); qerr != nil {
			if qerr == sql.ErrNoRows {
				continue // no row for this height (shouldn't happen within [0,tip])
			}
			return updated, skipped, fmt.Errorf("BackfillAccountNonces: probe block %d: %w", n, qerr)
		}
		if present {
			skipped++
			continue
		}

		blk, berr := GetZKBlockByNumber(nil, n)
		if berr != nil {
			return updated, skipped, fmt.Errorf("BackfillAccountNonces: read block %d: %w", n, berr)
		}
		if len(blk.Transactions) == 0 {
			skipped++ // genesis / empty block touches no accounts
			continue
		}

		// Touched-address set — mirrors DB_OPs.EnrichBlockAccountNonces exactly:
		// every distinct sender, receiver, and contract-creation address, in
		// first-seen order. Apply reads AccountNonces into a map keyed by address,
		// so list order does not affect correctness.
		seen := make(map[common.Address]struct{})
		ordered := make([]common.Address, 0, len(blk.Transactions)*2)
		touch := func(a *common.Address) {
			if a == nil {
				return
			}
			if _, ok := seen[*a]; ok {
				return
			}
			seen[*a] = struct{}{}
			ordered = append(ordered, *a)
		}
		for i := range blk.Transactions {
			touch(blk.Transactions[i].From)
			touch(blk.Transactions[i].To)
			// Contract deployment (To == nil): the created account lives at the
			// CREATE-deterministic address, the same one the EVM computes at apply.
			if blk.Transactions[i].To == nil && blk.Transactions[i].From != nil {
				ca := crypto.CreateAddress(*blk.Transactions[i].From, blk.Transactions[i].Nonce)
				touch(&ca)
			}
		}

		out := make([]config.AccountNonce, 0, len(ordered))
		for _, addr := range ordered {
			doc, gerr := GetAccount(nil, addr)
			if gerr != nil || doc == nil {
				return updated, skipped, fmt.Errorf(
					"BackfillAccountNonces: block %d touched account %s absent from accounts store (chain inconsistency — run on the sequencer): %v",
					n, addr.Hex(), gerr)
			}
			out = append(out, config.AccountNonce{Address: addr, Nonce: doc.Nonce})
		}

		raw, merr := json.Marshal(out)
		if merr != nil {
			return updated, skipped, fmt.Errorf("BackfillAccountNonces: marshal block %d: %w", n, merr)
		}
		// Stored as a JSON STRING value under extra_data.account_nonces, matching
		// the write path (backend/block.go) and read path (thebe_conversions.go).
		if _, uerr := sqlDB.ExecContext(ctx,
			`UPDATE blocks SET extra_data = jsonb_set(coalesce(extra_data, '{}'::jsonb), '{account_nonces}', to_jsonb($1::text), true) WHERE block_number = $2`,
			string(raw), n,
		); uerr != nil {
			return updated, skipped, fmt.Errorf("BackfillAccountNonces: update block %d: %w", n, uerr)
		}
		updated++
	}
	return updated, skipped, nil
}
