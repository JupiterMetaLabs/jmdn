package Block

import (
	"encoding/hex"
	"encoding/json"
	"gossipnode/DB_OPs"
	"gossipnode/SmartContract"
	"io/ioutil"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
)

// Add new types for contract compilation requests
type CompileContractRequest struct {
	SourceCode string `json:"source_code" binding:"required"`
	Name       string `json:"name" binding:"required"`
}

type DeployContractRequest struct {
	Bytecode    string `json:"bytecode" binding:"required"`
	ABI         string `json:"abi" binding:"required"`
	From        string `json:"from" binding:"required"`
	Value       string `json:"value" binding:"omitempty"`
	GasLimit    uint64 `json:"gas_limit" binding:"required"`
	Constructor string `json:"constructor" binding:"omitempty"`
}

type ExecuteContractRequest struct {
	ContractAddress string `json:"contract_address" binding:"required"`
	ABI             string `json:"abi" binding:"required"`
	Method          string `json:"method" binding:"required"`
	From            string `json:"from" binding:"required"`
	Value           string `json:"value" binding:"omitempty"`
	GasLimit        uint64 `json:"gas_limit" binding:"required"`
	Params          string `json:"params" binding:"omitempty"`
}

// compileContract handles Solidity compilation requests
func compileContract(c *gin.Context) {
	var req CompileContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create a temporary file for the source code
	tempDir, err := ioutil.TempDir("", "solidity")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create temporary directory"})
		return
	}
	defer os.RemoveAll(tempDir)

	sourceFile := filepath.Join(tempDir, req.Name+".sol")
	if err := ioutil.WriteFile(sourceFile, []byte(req.SourceCode), 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write source file"})
		return
	}

	// Compile the contract
	contracts, err := SmartContract.CompileSolidity(sourceFile)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, contracts)
}

// deployContract handles smart contract deployment
func deployContract(c *gin.Context) {
	var req DeployContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Remove 0x prefix if present
	bytecodeHex := strings.TrimPrefix(req.Bytecode, "0x")

	// Parse bytecode
	bytecode, err := hex.DecodeString(bytecodeHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bytecode"})
		return
	}

	// Create transaction
	caller := common.HexToAddress(req.From)
	value := big.NewInt(0)
	if req.Value != "" {
		var ok bool
		value, ok = new(big.Int).SetString(req.Value, 10)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid value"})
			return
		}
	}

	// Connect to DB
	mainDBClient, err := DB_OPs.GetMainDBConnection()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer DB_OPs.PutMainDBConnection(mainDBClient)

	// Create state DB and EVM
	stateDB := SmartContract.NewImmuStateDB(mainDBClient)
	executor := SmartContract.NewEVMExecutor(globalChainID)

	// Deploy contract
	result, err := executor.DeployContract(stateDB, caller, bytecode, value, req.GasLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Type assert to access the Commit method
	immuStateDB, ok := stateDB.(*SmartContract.ImmuStateDB)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to convert state DB"})
		return
	}

	// Commit state changes
	stateRoot, err := immuStateDB.Commit()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to commit state changes"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"contract_address": result.ContractAddr.Hex(),
		"gas_used":         result.GasUsed,
		"state_root":       stateRoot.Hex(),
	})
}

// executeContract handles smart contract method execution
func executeContract(c *gin.Context) {
	var req ExecuteContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Parse ABI
	parsedABI, err := SmartContract.ParseABI(req.ABI)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ABI: " + err.Error()})
		return
	}

	// Parse method params
	var params []interface{}
	if req.Params != "" {
		if err := json.Unmarshal([]byte(req.Params), &params); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid method parameters: " + err.Error()})
			return
		}
	}

	// Pack method call data
	input, err := parsedABI.Pack(req.Method, params...)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to pack method call: " + err.Error()})
		return
	}

	// Create transaction
	caller := common.HexToAddress(req.From)
	contractAddr := common.HexToAddress(req.ContractAddress)
	value := big.NewInt(0)
	if req.Value != "" {
		var ok bool
		value, ok = new(big.Int).SetString(req.Value, 10)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid value"})
			return
		}
	}

	// Connect to DB
	mainDBClient, err := DB_OPs.GetMainDBConnection()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database connection failed"})
		return
	}
	defer DB_OPs.PutMainDBConnection(mainDBClient)

	// Create state DB and EVM
	stateDB := SmartContract.NewImmuStateDB(mainDBClient)
	executor := SmartContract.NewEVMExecutor(globalChainID)

	// Execute contract method
	result, err := executor.ExecuteContract(stateDB, caller, contractAddr, input, value, req.GasLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	immuStateDB, ok := stateDB.(*SmartContract.ImmuStateDB)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to convert state DB"})
		return
	}

	// Commit state changes
	stateRoot, err := immuStateDB.Commit()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to commit state changes"})
		return
	}

	// Try to unpack the result
	var unpacked interface{}
	method, exist := parsedABI.Methods[req.Method]
	if exist && len(method.Outputs) > 0 {
		unpacked, err = method.Outputs.Unpack(result.ReturnData)
		if err != nil {
			unpacked = hex.EncodeToString(result.ReturnData)
		}
	} else {
		unpacked = hex.EncodeToString(result.ReturnData)
	}

	c.JSON(http.StatusOK, gin.H{
		"result":     unpacked,
		"gas_used":   result.GasUsed,
		"state_root": stateRoot.Hex(),
	})
}
