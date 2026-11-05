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
// the consensus settings. An address that is set but not a hex address is an
// error: with it, every block from the activation height on would be refused.
func headerBindingFromSettings(c settings.ConsensusSettings) (Security.HeaderBindingConfig, error) {
	out := Security.HeaderBindingConfig{
		FromHeight:     c.HeaderBindingFromHeight,
		MaxFutureDrift: time.Duration(c.BlockTimestampMaxFutureSeconds) * time.Second,
	}
	parse := func(key, v string) (*common.Address, error) {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, nil
		}
		if !common.IsHexAddress(v) {
			return nil, fmt.Errorf("consensus.%s %q is not a hex address", key, v)
		}
		a := common.HexToAddress(v)
		return &a, nil
	}
	var err error
	if out.Coinbase, err = parse("coinbase_address", c.CoinbaseAddress); err != nil {
		return out, err
	}
	if out.ZKVM, err = parse("zkvm_address", c.ZKVMAddress); err != nil {
		return out, err
	}
	return out, nil
}
