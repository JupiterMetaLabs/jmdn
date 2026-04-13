package evm

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// NewChainConfig returns a standard chain configuration
// In a real system, this might load from a config file or genesis block
func NewChainConfig(chainID int) *params.ChainConfig {
	// Explicitly enable all forks from genesis (Time/Block 0) to support modern opcodes (PUSH0, etc.)
	zero := big.NewInt(0)
	zeroTime := uint64(0)
	fmt.Printf("DEBUG: NewChainConfig called for ChainID %d. ShanghaiTime: %d, CancunTime: %v\n", chainID, zeroTime, nil)

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
// NewVMConfig returns the VM configuration
func NewVMConfig() vm.Config {
	fmt.Println("DEBUG: NewVMConfig called")
	return vm.Config{
		NoBaseFee: true,        // Disable EIP-1559 base fee checks
		ExtraEips: []int{3855}, // Force enable EIP-3855 (PUSH0)
	}
}
