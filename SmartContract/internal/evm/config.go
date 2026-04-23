package evm

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// NewChainConfig returns a standard chain configuration for the JMDN EVM.
// All forks up to Shanghai are enabled at genesis (block/time 0); Cancun is
// disabled to avoid blob-gas complexity until we're ready to support it.
func NewChainConfig(chainID int) *params.ChainConfig {
	zero := big.NewInt(0)
	zeroTime := uint64(0)

	return &params.ChainConfig{
		ChainID:                       big.NewInt(int64(chainID)),
		HomesteadBlock:                zero,
		DAOForkBlock:                  zero,
		DAOForkSupport:                true,
		EIP150Block:                   zero,
		EIP155Block:                   zero,
		EIP158Block:                   zero,
		ByzantiumBlock:                zero,
		ConstantinopleBlock:           zero,
		PetersburgBlock:               zero,
		IstanbulBlock:                 zero,
		MuirGlacierBlock:              zero,
		BerlinBlock:                   zero,
		LondonBlock:                   zero,
		ArrowGlacierBlock:             zero,
		GrayGlacierBlock:              zero,
		MergeNetsplitBlock:            zero,          // The Merge
		TerminalTotalDifficulty:       big.NewInt(0), // Required for Merge to be active at genesis
		ShanghaiTime:                  &zeroTime,     // Enable Shanghai (PUSH0)
		CancunTime:                    nil,           // Disable Cancun to avoid blob gas issues
	}
}

// NewVMConfig returns the VM configuration
func NewVMConfig() vm.Config {
	return vm.Config{
		NoBaseFee: true,        // Disable EIP-1559 base fee checks
		ExtraEips: []int{3855}, // Force enable EIP-3855 (PUSH0)
	}
}
