package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/Security"
	"gossipnode/config/settings"
)

// headerBindingFromSettings builds the Security header-binding policy from
// the consensus settings. It fails when the policy would be unsafe or
// ambiguous: an address that is not a hex address, ranges not in strictly
// increasing from_height order, or, with binding on, no address range
// covering the activation height (the address rule would be off and the
// sequencer could pick its own fee recipients).
func headerBindingFromSettings(c settings.ConsensusSettings) (Security.HeaderBindingConfig, error) {
	out := Security.HeaderBindingConfig{
		FromHeight:     c.HeaderBindingFromHeight,
		MaxFutureDrift: time.Duration(c.BlockTimestampMaxFutureSeconds) * time.Second,
	}
	parse := func(i int, key, v string) (common.Address, error) {
		v = strings.TrimSpace(v)
		if !common.IsHexAddress(v) {
			return common.Address{}, fmt.Errorf("consensus.header_addresses[%d].%s %q is not a hex address", i, key, v)
		}
		return common.HexToAddress(v), nil
	}
	for i, r := range c.HeaderAddresses {
		cb, err := parse(i, "coinbase", r.Coinbase)
		if err != nil {
			return out, err
		}
		zk, err := parse(i, "zkvm", r.ZKVM)
		if err != nil {
			return out, err
		}
		if i > 0 && r.FromHeight <= c.HeaderAddresses[i-1].FromHeight {
			return out, fmt.Errorf("consensus.header_addresses must be in strictly increasing from_height order (entry %d)", i)
		}
		out.Addresses = append(out.Addresses, Security.AddressRange{FromHeight: r.FromHeight, Coinbase: cb, ZKVM: zk})
	}
	// Legacy-follow blocks (blocks from the main-branch sequencer) carry the
	// transactions-only BlockHash, so the legacy window must end below the
	// header-binding height or every such block would fail the hash check.
	if lf := c.LegacyFollow; out.FromHeight > 0 && lf.Enabled && (lf.UntilHeight == 0 || lf.UntilHeight >= out.FromHeight) {
		return out, fmt.Errorf("consensus.legacy_follow.until_height must be set below consensus.header_binding_from_height %d (got %d)", out.FromHeight, lf.UntilHeight)
	}
	if out.FromHeight > 0 && (len(out.Addresses) == 0 || out.Addresses[0].FromHeight > out.FromHeight) {
		return out, fmt.Errorf("consensus.header_binding_from_height %d is set but consensus.header_addresses has no entry starting at or below it", out.FromHeight)
	}
	return out, nil
}
