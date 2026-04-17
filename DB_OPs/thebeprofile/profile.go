package thebeprofile

import (
	"database/sql"
	"fmt"
)

const ProfileName = "jmdn"

var namespaces = []string{"account", "block", "tx", "zk", "snapshot"}

type JMDNProfile struct{}

func New() *JMDNProfile { return &JMDNProfile{} }

func (p *JMDNProfile) Name() string         { return ProfileName }
func (p *JMDNProfile) Namespaces() []string { return namespaces }
func (p *JMDNProfile) GetMigration() string { return migration }

func (p *JMDNProfile) Apply(tx *sql.Tx, namespace, _ string, value []byte) error {
	switch namespace {
	case "account":
		return applyAccount(tx, value)
	case "block":
		return applyBlock(tx, value)
	case "tx":
		return applyTx(tx, value)
	case "zk":
		return applyZKProof(tx, value)
	case "snapshot":
		return applySnapshot(tx, value)
	default:
		return fmt.Errorf("thebeprofile: unknown namespace %q", namespace)
	}
}
