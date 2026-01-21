package evm

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// NewChainConfig returns a standard chain configuration
// In a real system, this might load from a config file or genesis block
func NewChainConfig(chainID int) *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:             big.NewInt(int64(chainID)),
		HomesteadBlock:      big.NewInt(0),
		EIP150Block:         big.NewInt(0),
		EIP155Block:         big.NewInt(0),
		EIP158Block:         big.NewInt(0),
		ByzantiumBlock:      big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
		PetersburgBlock:     big.NewInt(0),
		IstanbulBlock:       big.NewInt(0),
		BerlinBlock:         big.NewInt(0),
		LondonBlock:         big.NewInt(0),
	}
}

// NewVMConfig returns the VM configuration
func NewVMConfig() vm.Config {
	return vm.Config{
		NoBaseFee: true, // Disable EIP-1559 base fee for simplicity in this implementation
	}
}
