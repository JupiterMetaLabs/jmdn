//go:build applygate

package BlockProcessing_test

import "gossipnode/internal/applygate"

// Fixture bytecode lives in the shared harness (internal/applygate/fixtures.go),
// generated from testdata/InternalCreate.sol.
const (
	binBomb       = applygate.BinBomb
	binChild      = applygate.BinChild
	binFactory    = applygate.BinFactory
	binForward    = applygate.BinForward
	binRegistry   = applygate.BinRegistry
	binSettlement = applygate.BinSettlement
)
