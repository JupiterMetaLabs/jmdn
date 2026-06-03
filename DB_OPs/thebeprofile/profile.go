// MODULE: DB_OPs/thebeprofile/profile.go
// PURPOSE: JMDNProfile implements ThebeDB's profile.Profile — projects CanonicalRecords
//          from the KV log into the JMDN PostgreSQL schema (6 tables).
//
// CORE DATA STRUCTURES:
//   - handlers: map[string]applyFunc — keyed by namespace string, populated once at
//     construction in NewJMDNProfile(), read-only after. Access: O(1) lookup at Apply() time.
//     Size: fixed (6 entries — one per SQL namespace). No locking needed (read-only after init).
//
// TO MODIFY BEHAVIOR:
//   - Add new SQL namespace: add applyFunc + register in NewJMDNProfile() handlers map
//   - Change SQL for existing namespace: edit the corresponding apply_<entity>.go file
//
// DO NOT:
//   - Use reflection to build SQL arguments (type-unsafe, breaks on rename)
//   - Import gossipnode/DB_OPs (cycle risk — thebeprofile sits inside DB_OPs/)
//   - Store mutable state on JMDNProfile (Apply() is called concurrently)
//
// EXTENSION POINT: new namespace → new apply_<entity>.go + register in handlers map
//
// CHANGE SCENARIOS:
//   Add contract namespaces (Phase 7): add apply_contract_*.go + register — profile.go unchanged
//   Change account upsert logic: edit apply_account.go — profile.go unchanged

package thebeprofile

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	core "github.com/JupiterMetaLabs/ThebeDB/pkg/core"
	profilepkg "github.com/JupiterMetaLabs/ThebeDB/pkg/profile"

	"gossipnode/DB_OPs/thebegateway"
)

// compile-time interface check
var _ profilepkg.Profile = (*JMDNProfile)(nil)

// applyFunc is the typed handler signature for each namespace.
// seq is the KV log sequence number; record carries namespace + raw value bytes.
// tx is per-call — never share across goroutines.
type applyFunc func(ctx context.Context, seq uint64, record *core.CanonicalRecord, tx *sql.Tx) error

// JMDNProfile projects CanonicalRecords into the JMDN PostgreSQL schema.
// Safe for concurrent use — handlers map is read-only after NewJMDNProfile().
type JMDNProfile struct {
	handlers map[string]applyFunc
}

// NewJMDNProfile constructs a JMDNProfile with all 7 namespace handlers registered.
func NewJMDNProfile() *JMDNProfile {
	p := &JMDNProfile{
		handlers: make(map[string]applyFunc, 7),
	}
	p.handlers["account"] = applyAccount
	p.handlers["block"] = applyBlock
	p.handlers["snapshot"] = applySnapshot
	p.handlers["tx"] = applyTransaction
	p.handlers["zk"] = applyZKProof
	p.handlers["l1_finality"] = applyL1Finality
	p.handlers[string(thebegateway.NamespaceContractReceipt)] = applyContractReceipt
	return p
}

// Name returns the unique profile identifier used for logging and offset tracking.
func (p *JMDNProfile) Name() string { return "jmdn" }

// Namespaces returns the exact Namespace values this profile handles.
// Must match the Namespace field set on records at write time — mismatch causes silent data loss.
func (p *JMDNProfile) Namespaces() []string {
	return []string{"account", "block", "snapshot", "tx", "zk", "l1_finality", "contract_receipt"}
}

// GetMigration returns the complete PostgreSQL DDL for the JMDN projection schema.
// Executed verbatim once on startup; all statements use IF NOT EXISTS for idempotency.
// Migration order: 000001_init_schema → 000002_contract_receipt
func (p *JMDNProfile) GetMigration() string {
	return migrationSQL + "\n\n" + migrationSQL002
}

// Apply routes a single CanonicalRecord to the correct namespace handler.
// Unknown namespaces are logged and silently skipped (return nil) to avoid
// blocking other namespace projections. Apply is safe for concurrent use.
// Time: O(1) — map lookup into handlers (fixed-size, 7 entries); SQL cost is in the apply func
func (p *JMDNProfile) Apply(ctx context.Context, seq uint64, record *core.CanonicalRecord, tx *sql.Tx) error {
	if record == nil {
		return nil
	}
	fn, ok := p.handlers[record.Namespace]
	if !ok {
		log.Printf("thebeprofile: unknown namespace %q seq=%d — skipping", record.Namespace, seq)
		return nil
	}
	if err := fn(ctx, seq, record, tx); err != nil {
		return fmt.Errorf("thebeprofile: namespace=%q seq=%d: %w", record.Namespace, seq, err)
	}
	return nil
}
