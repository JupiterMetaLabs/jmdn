package settings

import (
	"fmt"
	"strings"
)

// Canonical chain IDs (registered on ethereum-lists/chains).
const (
	MainnetChainID = 7000700
	TestnetChainID = 8000800
)

// Network environments a node may declare.
const (
	EnvMainnet = "mainnet"
	EnvTestnet = "testnet"
	EnvDevnet  = "devnet"
)

// ValidateEnvironment rejects an environment the node cannot interpret and a
// non-production environment on the mainnet chain.
//
// network.environment turns production safety locks on ("mainnet") or off
// (anything else), so it must not be usable as a quiet way to run a mainnet
// node without them: a testnet/devnet environment on chain_id 7000700 refuses
// to boot. Running mainnet locks on another chain is allowed (stricter).
func (c *NodeConfig) ValidateEnvironment() error {
	env := strings.ToLower(strings.TrimSpace(c.Network.Environment))
	switch env {
	case EnvMainnet, EnvTestnet, EnvDevnet:
	default:
		return fmt.Errorf("network.environment %q is not one of mainnet, testnet, devnet", c.Network.Environment)
	}
	if env != EnvMainnet && c.Network.ChainID == MainnetChainID {
		return fmt.Errorf("network.environment %q with the mainnet chain_id %d: production safety locks would be off on mainnet — use environment: mainnet (or security.strict_posture) on this chain", env, MainnetChainID)
	}
	return nil
}

// ProductionPosture reports whether production safety locks apply: strict
// posture set, or a mainnet environment.
func (c *NodeConfig) ProductionPosture() bool {
	return c.Security.StrictPosture || strings.EqualFold(strings.TrimSpace(c.Network.Environment), EnvMainnet)
}
