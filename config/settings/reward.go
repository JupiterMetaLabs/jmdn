package settings

import (
	"fmt"
	"regexp"
	"strings"
)

// hexAddressRe matches a 20-byte hex Ethereum-style address (0x + 40 hex
// digits). Equivalent to go-ethereum's common.IsHexAddress, inlined here so the
// settings package keeps no dependency on go-ethereum. See
// docs/STAKING-REWARDS-DESIGN.md.
var hexAddressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// ValidateRewardAddress checks the OPTIONAL consensus.reward_address. Empty is
// valid (the node claims no gas-fee reward). A non-empty value must be a
// well-formed 20-byte hex address; a malformed one is rejected at boot rather
// than silently normalized (common.HexToAddress would pad/truncate junk, which
// could mis-credit rewards). Returns nil when valid.
func (c *NodeConfig) ValidateRewardAddress() error {
	addr := strings.TrimSpace(c.Consensus.RewardAddress)
	if addr == "" {
		return nil
	}
	if !hexAddressRe.MatchString(addr) {
		return fmt.Errorf("consensus.reward_address %q is not a valid 20-byte hex address (0x + 40 hex digits); leave it empty to claim no reward", addr)
	}
	return nil
}
