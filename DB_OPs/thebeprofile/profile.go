package thebeprofile

import (
	"context"
	"database/sql"
	"fmt"

	thebejmdt "github.com/JupiterMetaLabs/ThebeDB/examples/jmdt"
)

const ProfileName = "jmdn"

var namespaces = []string{"account", "block", "tx", "zk", "snapshot"}

type JMDNProfile struct{}

func New() *JMDNProfile { return &JMDNProfile{} }

func (p *JMDNProfile) Name() string         { return ProfileName }
func (p *JMDNProfile) Namespaces() []string { return namespaces }
func (p *JMDNProfile) GetMigration() string { return migration }

func (p *JMDNProfile) Apply(_ context.Context, _ uint64, record *thebejmdt.CanonicalRecord, tx *sql.Tx) error {
	if record == nil {
		return nil
	}
	switch record.Namespace {
	case "account":
		return applyAccount(tx, record.Value)
	case "block":
		return applyBlock(tx, record.Value)
	case "tx":
		return applyTx(tx, record.Value)
	case "zk":
		return applyZKProof(tx, record.Value)
	case "snapshot":
		return applySnapshot(tx, record.Value)
	default:
		return fmt.Errorf("thebeprofile: unknown namespace %q", record.Namespace)
	}
}
