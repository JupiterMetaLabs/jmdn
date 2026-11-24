package adapters

import (
	"fmt"
	"math/big"
)

// Network selects which chain id a node validates against. Deliberately a
// small closed set of strings, not a free-form field — an unrecognized value
// must be a loud config error, never a silent fallback to zero.
type Network string

const (
	NetworkMainnet Network = "mainnet"
	NetworkTestnet Network = "testnet"
)

// ChainIDConfig holds BOTH chain ids and picks one via Network.
//
// WHY BOTH ARE CARRIED, NOT JUST THE ACTIVE ONE: a single "chain_id" field
// (jmdn's current config/settings/config.go:107 shape) means switching
// networks means editing a number and hoping you typed the right one. Keeping
// both named values plus an explicit Network selector makes the choice
// self-documenting and lets Validate() catch someone editing the wrong field
// or leaving a used one blank.
//
// UNRESOLVED, CARRIED FROM THE PARITY LEDGER: jmdn currently ships ONE flat
// chain_id with two conflicting defaults across files — 8000800
// (config/settings/defaults.go:18) vs 7000700 (jmdn_default.yaml, which wins
// because YAML overrides code defaults). Neither is asserted here as
// "mainnet" or "testnet" — that mapping has not been confirmed. This struct
// has NO hardcoded chain id values; both fields default to nil, and Resolve()
// refuses to proceed with a nil value rather than guess. Populate
// MainnetChainID/TestnetChainID from a confirmed source (ops/lead) before use.
type ChainIDConfig struct {
	Network        Network
	MainnetChainID *big.Int
	TestnetChainID *big.Int
}

// Resolve returns the chain id for the configured Network, or an error.
// Fail-closed on every invalid state: unknown network, or a nil chain id for
// the selected network. A v3 vote/signature bound to a wrong or zero chain id
// would silently never verify against peers — a loud error here is far
// cheaper than debugging that in production.
func (c ChainIDConfig) Resolve() (*big.Int, error) {
	switch c.Network {
	case NetworkMainnet:
		if c.MainnetChainID == nil || c.MainnetChainID.Sign() <= 0 {
			return nil, fmt.Errorf("adapters.ChainIDConfig: network=%q but MainnetChainID is unset or non-positive", c.Network)
		}
		return new(big.Int).Set(c.MainnetChainID), nil
	case NetworkTestnet:
		if c.TestnetChainID == nil || c.TestnetChainID.Sign() <= 0 {
			return nil, fmt.Errorf("adapters.ChainIDConfig: network=%q but TestnetChainID is unset or non-positive", c.Network)
		}
		return new(big.Int).Set(c.TestnetChainID), nil
	case "":
		return nil, fmt.Errorf("adapters.ChainIDConfig: Network is empty, want %q or %q", NetworkMainnet, NetworkTestnet)
	default:
		return nil, fmt.Errorf("adapters.ChainIDConfig: unknown Network %q, want %q or %q", c.Network, NetworkMainnet, NetworkTestnet)
	}
}

// NewStatelessCheckerForNetwork resolves cfg's chain id and builds a
// StatelessChecker for it in one step — the entry point jmdn's startup code
// should call, so the mainnet/testnet decision and the checker construction
// can never drift apart (e.g. a checker accidentally built against a
// different value than what Resolve would have returned).
func NewStatelessCheckerForNetwork(cfg ChainIDConfig) (*StatelessChecker, error) {
	id, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	return NewStatelessChecker(id)
}
