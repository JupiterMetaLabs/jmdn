// MODULE: DB_OPs/genesis_seed
// PURPOSE: Bootstrap/test genesis allocation. JMDN has no in-protocol mint and no
// genesis-alloc file: native coin only ever MOVES between existing accounts, and
// accounts are created only by block application (see local_create_gate.go). That
// leaves no way to obtain the FIRST balance on a fresh chain. This seeder fills
// that gap for bootstrap and for the 2-node determinism gate.
//
// HOW: read a JSON file {"0xADDR":"balanceWei", ...} named by JMDN_GENESIS_ALLOC
// and, for each address not already present, create the account and set its
// balance — through the node's OWN handle (getHandle), so encoding + the
// zero-balance-clobber merge guard are identical to every other write.
//
// DETERMINISM (why this is safe for the P2.5 fingerprint): given the same file,
// every node produces the same (address, balance, TxNonce=0, TxCountSent=0)
// leaves. StateFingerprintV1 folds ONLY those fields — NOT the ART ordinal and
// NOT volatile timestamps — so identical files yield identical fingerprints
// fleet-wide. Accounts are seeded in sorted-address order so first-boot ART
// ordinals are also assigned identically. Seed BEFORE any block is produced or
// applied so the pre-genesis baseline matches across the fleet.
//
// GATING: requires JMDN_ALLOW_LOCAL_ACCOUNT_CREATE=1 (the same escape hatch that
// governs every out-of-band creation path). Idempotent: existing accounts are
// skipped, and the merge guard protects a funded account even if a create is
// retried.
package DB_OPs

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// GenesisAllocEnv is the env var naming the genesis allocation JSON file.
const GenesisAllocEnv = "JMDN_GENESIS_ALLOC"

// SeedGenesisFromEnv seeds from the file named by JMDN_GENESIS_ALLOC. It is a
// no-op (0, nil) when the variable is unset/empty, so it is safe to call
// unconditionally at startup.
func SeedGenesisFromEnv(ctx context.Context) (int, error) {
	path := strings.TrimSpace(os.Getenv(GenesisAllocEnv))
	if path == "" {
		return 0, nil
	}
	return SeedGenesisAllocations(ctx, path)
}

// SeedGenesisAllocations reads {"0xADDR":"balanceWei", ...} and seeds each
// missing account with its balance. Returns the number of accounts newly seeded.
func SeedGenesisAllocations(ctx context.Context, path string) (int, error) {
	if !AllowLocalAccountCreate {
		return 0, fmt.Errorf("genesis seed: %w", ErrLocalAccountCreateDisabled)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("genesis seed: read %s: %w", path, err)
	}
	var alloc map[string]string
	if err := json.Unmarshal(raw, &alloc); err != nil {
		return 0, fmt.Errorf("genesis seed: parse %s: %w", path, err)
	}

	// Sorted by lowercase hex → identical create order (hence identical first-boot
	// ART ordinals) on every node.
	addrs := make([]string, 0, len(alloc))
	for a := range alloc {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return strings.ToLower(addrs[i]) < strings.ToLower(addrs[j])
	})

	seeded := 0
	for _, a := range addrs {
		if !common.IsHexAddress(a) {
			return seeded, fmt.Errorf("genesis seed: %q is not a valid hex address", a)
		}
		addr := common.HexToAddress(a)

		bal := strings.TrimSpace(alloc[a])
		if bal == "" {
			bal = "0"
		}
		if _, ok := new(big.Int).SetString(bal, 10); !ok {
			return seeded, fmt.Errorf("genesis seed: %s: invalid decimal-wei balance %q", a, bal)
		}

		// Idempotent: skip accounts that already exist (GetAccount returns a
		// non-nil error — "key not found" — only when absent).
		if _, gErr := GetAccount(nil, addr); gErr == nil {
			continue
		}

		did := "did:jmdn:" + strings.ToLower(addr.Hex())
		if cErr := CreateAccount(nil, did, addr, nil); cErr != nil {
			return seeded, fmt.Errorf("genesis seed: create %s: %w", a, cErr)
		}
		if uErr := UpdateAccountBalance(nil, addr, bal, 0); uErr != nil {
			return seeded, fmt.Errorf("genesis seed: set balance %s: %w", a, uErr)
		}
		seeded++
	}
	return seeded, nil
}
