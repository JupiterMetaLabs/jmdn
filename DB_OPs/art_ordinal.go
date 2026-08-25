// MODULE: DB_OPs/art_ordinal
// PURPOSE: Globally consistent ART identity nonces for accounts, assigned as a
// monotonic creation ordinal by the sequencer and carried in each block.
//
// WHY: the ART nonce (Account.Nonce) is the Fastsync AccountSync set key — the
// account-set diff, the sorted-delta wire encoding, and the SwappableART
// segment binary search are all keyed by it. Historically it was minted
// locally at creation (GenerateARTNonce: time+counter, per node). That was
// consistent only while accounts were created ONCE and propagated; with
// receiver accounts created independently at block apply on every node, each
// node minted a different nonce for the same account, so the AccountSync diff
// never matched and accounts were re-transferred on every catch-up.
//
// NOW: the sequencer stamps every distinct sender/receiver in a block with its
// canonical nonce (EnrichBlockAccountNonces, called before consensus):
//   - existing account → its stored nonce (heals nodes that drifted: at apply,
//     a node holding a different value ADOPTS the carried one), and
//   - account the block itself creates → the next value of a persisted,
//     monotonic ordinal counter (dense 1,2,3…, so the AccountSync sorted-delta
//     encoding stays tiny).
// Every node applies the same block → every node writes the identical nonce.
//
// ORDINAL SPACE: ordinals live strictly below ARTOrdinalMax (2^40). Legacy
// GenerateARTNonce values are ≈ (µs<<12) ≈ 7e18 — far above — so the two
// ranges can never collide, and apply-side floor maintenance can distinguish
// "carried ordinal" (bump the local floor for sequencer failover) from
// "carried legacy nonce" (ignore for the floor).
//
// FAILOVER: every node bumps its persisted counter floor as it applies blocks
// (BumpARTOrdinalFloor), so a node promoted to sequencer continues the
// sequence without reuse. Reservation persists the advanced counter BEFORE
// ordinals are handed out — a crash mid-block leaks a gap (harmless), never a
// duplicate.

package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// ARTOrdinalMax is the exclusive upper bound of the sequencer-assigned ordinal
// space (2^40 ≈ 1.1e12 accounts). Values at or above it are legacy
// GenerateARTNonce mints and are never treated as ordinals.
const ARTOrdinalMax = uint64(1) << 40

// artOrdinalKey is the sync-state KV key holding the NEXT unassigned ordinal.
// Namespaced under "sync:" like AppliedAnchorKey; persisted via the ThebeDB
// handle's GetSyncKV/PutSyncKV (the old "art_ordinal_next" main-DB key was an
// ImmuDB-era Read/Update path that no longer persists — see the rewrite below).
const artOrdinalKey = "sync:art_ordinal_next"

// artOrdinalMu serializes read-reserve-write on the counter within this
// process (the sequencer assigns from one goroutine, but apply-side floor
// bumps run concurrently with block building on the same node).
var artOrdinalMu sync.Mutex

// readARTOrdinalNext returns the persisted next-ordinal value, or 1 when the
// key does not exist yet (ordinal space starts at 1; 0 is reserved as the
// "no identity carried" sentinel that mergeAccountForWrite already preserves).
func readARTOrdinalNext() (uint64, error) {
	h, err := getHandle(nil)
	if err != nil {
		return 0, fmt.Errorf("art_ordinal read: %w", err)
	}
	raw, err := h.GetSyncKV(artOrdinalKey)
	if err != nil {
		return 0, fmt.Errorf("art_ordinal read: %w", err)
	}
	if raw == nil {
		// Never seeded (fresh chain) → ordinal space starts at 1. GetSyncKV
		// returns (nil, nil) for an absent key, so this needs no error sentinel.
		return 1, nil
	}
	var next uint64
	if err := json.Unmarshal(raw, &next); err != nil {
		return 0, fmt.Errorf("art_ordinal parse: %w", err)
	}
	if next == 0 {
		next = 1
	}
	return next, nil
}

// writeARTOrdinalNext persists the next-ordinal counter through the ThebeDB
// sync-state KV (mirrors writeAnchorLocked). Callers hold artOrdinalMu.
func writeARTOrdinalNext(v uint64) error {
	h, err := getHandle(nil)
	if err != nil {
		return fmt.Errorf("art_ordinal write: %w", err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("art_ordinal encode: %w", err)
	}
	if err := h.PutSyncKV(artOrdinalKey, raw); err != nil {
		return fmt.Errorf("art_ordinal write: %w", err)
	}
	return nil
}

// reserveARTOrdinals atomically reserves n consecutive ordinals and returns
// the first. The advanced counter is persisted BEFORE the ordinals are handed
// out, so a crash can only leak a gap, never double-assign.
func reserveARTOrdinals(n uint64) (uint64, error) {
	if n == 0 {
		return 0, fmt.Errorf("art_ordinal reserve: n must be > 0")
	}
	artOrdinalMu.Lock()
	defer artOrdinalMu.Unlock()

	next, err := readARTOrdinalNext()
	if err != nil {
		return 0, err
	}
	if next+n >= ARTOrdinalMax {
		return 0, fmt.Errorf("art_ordinal reserve: ordinal space exhausted (next=%d, n=%d)", next, n)
	}
	if err := writeARTOrdinalNext(next + n); err != nil {
		return 0, fmt.Errorf("art_ordinal reserve: persist %d: %w", next+n, err)
	}
	return next, nil
}

// BumpARTOrdinalFloor raises the persisted counter to at least floor. Called
// on the APPLY path with (highest carried ordinal)+1 so that any node — should
// it later become the sequencer — resumes the sequence past every ordinal it
// has ever seen. Values outside the ordinal space are ignored. Best-effort:
// the caller treats errors as non-fatal (the floor is failover insurance, not
// apply correctness).
func BumpARTOrdinalFloor(floor uint64) error {
	if floor == 0 || floor >= ARTOrdinalMax {
		return nil
	}
	artOrdinalMu.Lock()
	defer artOrdinalMu.Unlock()

	next, err := readARTOrdinalNext()
	if err != nil {
		return err
	}
	if floor <= next {
		return nil
	}
	if err := writeARTOrdinalNext(floor); err != nil {
		return fmt.Errorf("art_ordinal floor: persist %d: %w", floor, err)
	}
	return nil
}

// EnrichBlockAccountNonces populates block.AccountNonces with the canonical
// ART nonce for every distinct sender and receiver in the block:
//
//   - account present in accountsdb → its stored nonce (apply-side ADOPT heals
//     nodes holding a different value), and
//   - account missing (this block creates it) → a freshly reserved ordinal,
//     assigned in ascending-address order so the assignment is reproducible.
//
// SEQUENCER-ONLY, called between block validation and consensus start. The
// field is advisory (not part of the canonical block hash), so enrichment does
// not invalidate BlockHash. Fail-closed: any DB error aborts enrichment and
// the caller must reject the block — proposing a block that creates accounts
// WITHOUT carried identities would push every node back to local minting.
func EnrichBlockAccountNonces(block *config.ZKBlock) error {
	if block == nil {
		return fmt.Errorf("enrich account nonces: nil block")
	}
	if len(block.Transactions) == 0 {
		block.AccountNonces = nil
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := GetAccountConnectionandPutBack(ctx)
	if err != nil {
		return fmt.Errorf("enrich account nonces: connection: %w", err)
	}
	defer PutAccountsConnection(conn)

	// Distinct touched addresses, first-seen order preserved for the carried
	// list; missing ones are ordinal-assigned in ascending-address order.
	seen := make(map[common.Address]struct{})
	ordered := make([]common.Address, 0, len(block.Transactions)*2)
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
	for i := range block.Transactions {
		touch(block.Transactions[i].From)
		touch(block.Transactions[i].To)
		// Contract deployment (To == nil): the tx creates a NEW contract account at
		// the CREATE-deterministic address crypto.CreateAddress(sender, tx.Nonce) —
		// the same address the EVM computes at apply. Stamp it so it receives a
		// canonical monotonic ART ordinal like any other new account; validators
		// create the contract's ledger account from this carried identity (EVM P2).
		if block.Transactions[i].To == nil && block.Transactions[i].From != nil {
			ca := crypto.CreateAddress(*block.Transactions[i].From, block.Transactions[i].Nonce)
			touch(&ca)
		}
	}

	nonces := make(map[common.Address]uint64, len(ordered))
	missing := make([]common.Address, 0)
	for _, addr := range ordered {
		doc, err := GetAccount(conn, addr)
		switch {
		case err == nil && doc != nil && doc.Nonce != 0:
			nonces[addr] = doc.Nonce
		case err == nil && doc != nil:
			// Stored identity is the 0 sentinel ("no identity information" — e.g. a
			// reconciliation-created stub). Stamping 0 as canonical would FREEZE it:
			// adoptCarriedNonce deliberately skips 0, so no node could ever heal the
			// account, and multiple 0-keyed accounts collide in nonce-set lookups.
			// Treat it as missing instead: assign a real ordinal, which apply-side
			// adopt then propagates fleet-wide (including back onto this sequencer).
			missing = append(missing, addr)
		case err != nil && strings.Contains(err.Error(), "key not found"):
			missing = append(missing, addr)
		default:
			// Fail-closed: a transient DB error must not be read as "new account".
			return fmt.Errorf("enrich account nonces: read %s: %w", addr.Hex(), err)
		}
	}

	if len(missing) > 0 {
		// Deterministic assignment order: ascending address bytes.
		sort.Slice(missing, func(i, j int) bool {
			return strings.Compare(missing[i].Hex(), missing[j].Hex()) < 0
		})
		first, err := reserveARTOrdinals(uint64(len(missing)))
		if err != nil {
			return err
		}
		for i, addr := range missing {
			nonces[addr] = first + uint64(i)
		}
	}

	out := make([]config.AccountNonce, 0, len(ordered))
	for _, addr := range ordered {
		out = append(out, config.AccountNonce{Address: addr, Nonce: nonces[addr]})
	}
	block.AccountNonces = out
	return nil
}
