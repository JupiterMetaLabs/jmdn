package Types

// CompiledContract holds stateless compilation results from a Solidity source file.
// It contains only the essential compilation artifacts needed for deployment, execution, and verification.
// This struct does not perform any I/O operations (no file saving, no chain interaction).
//
// Usage flow:
//  1. CompileSolidity() → produces CompiledContract (stateless compilation)
//  2. Caller uses Bytecode → for contract deployment
//  3. Caller uses ABI → for encoding/decoding function calls
//  4. Caller uses DeployedBytecode → for verification purposes
type CompiledContract struct {
	// Bytecode is the full deployment bytecode including constructor code.
	// Contains: Full bytecode with constructor and initialization code.
	// Usage: Provided to caller for contract deployment operations.
	// Example: "0x6080604052348015600f57600080fd5b50..."
	Bytecode string `json:"bytecode"`

	// ABI is the Application Binary Interface as a JSON string.
	// Contains: JSON array describing functions, events, errors, and their types.
	// Usage:
	//   - Caller can use ParseABI() to create an abi.ABI object
	//   - Encodes function calls: parsedABI.Pack("methodName", params...)
	//   - Decodes return values and events
	//   - Required for contract interaction
	// Example: [{"type":"function","name":"transfer","inputs":[...],"outputs":[...]}]
	ABI string `json:"abi"`

	// DeployedBytecode is the runtime bytecode (what would be stored in a contract account).
	// Contains: Bytecode without constructor code.
	// Usage:
	//   - Represents the code that would be stored in a contract account after deployment
	//   - Can be used by caller for verification purposes
	//   - Can be used to verify deployed contract code matches compilation
	//   - Shorter than Bytecode (no constructor)
	// Example: "0x6080604052348015600f57600080fd5b50..." (runtime only)
	DeployedBytecode string `json:"deployed_bytecode"`
}

func NewCompiledContract(compiledContract *CompiledContract) *CompiledContract {
	if compiledContract == nil {
		return &CompiledContract{}
	}
	return &CompiledContract{
		Bytecode: compiledContract.Bytecode,
		ABI: compiledContract.ABI,
		DeployedBytecode: compiledContract.DeployedBytecode,
	}
}

func (c *CompiledContract) SetBytecode(bytecode string) *CompiledContract {
	c.Bytecode = bytecode
	return c
}
func (c *CompiledContract) SetABI(abi string) *CompiledContract {
	c.ABI = abi
	return c
}

func (c *CompiledContract) SetDeployedBytecode(deployedBytecode string) *CompiledContract {
	c.DeployedBytecode = deployedBytecode
	return c
}

func (c *CompiledContract) GetBytecode() string {
	return c.Bytecode
}

func (c *CompiledContract) GetABI() string {
	return c.ABI
}

func (c *CompiledContract) GetDeployedBytecode() string {
	return c.DeployedBytecode
}