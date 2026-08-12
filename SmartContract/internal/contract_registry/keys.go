package contract_registry

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

const (
	// Base prefix for all registry keys
	PrefixRegistry = "contract:registry:"

	// Prefix for contract metadata: contract:registry:<address>
	PrefixContract = PrefixRegistry + ""

	// Indexes
	// contract:registry:index:deployer:<deployer_addr>:<timestamp> -> <contract_addr>
	PrefixIndexDeployer = PrefixRegistry + "index:deployer:"

	// contract:registry:index:time:<timestamp>:<contract_addr> -> <contract_addr>
	PrefixIndexTime = PrefixRegistry + "index:time:"
)

// KeyContract returns the database key for a contract's metadata
func KeyContract(address common.Address) string {
	return fmt.Sprintf("%s%s", PrefixContract, strings.ToLower(address.Hex()))
}

// KeyIndexByDeployer returns the index key for listing by deployer
func KeyIndexByDeployer(deployer common.Address, timestamp int64, contractAddr common.Address) string {
	return fmt.Sprintf("%s%s:%d:%s", PrefixIndexDeployer, strings.ToLower(deployer.Hex()), timestamp, strings.ToLower(contractAddr.Hex()))
}

// KeyIndexByTime returns the index key for listing by time
func KeyIndexByTime(timestamp int64, contractAddr common.Address) string {
	return fmt.Sprintf("%s%d:%s", PrefixIndexTime, timestamp, strings.ToLower(contractAddr.Hex()))
}
