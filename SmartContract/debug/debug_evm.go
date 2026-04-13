package main

import (
	"fmt"
	"math/big"

	"gossipnode/SmartContract/internal/evm"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// MockStateDB implements vm.StateDB for debugging
type MockStateDB struct {
	balances map[common.Address]*uint256.Int
	nonces   map[common.Address]uint64
	code     map[common.Address][]byte
	storage  map[common.Address]map[common.Hash]common.Hash
	logs     []*types.Log
	refund   uint64
}

func NewMockStateDB() *MockStateDB {
	return &MockStateDB{
		balances: make(map[common.Address]*uint256.Int),
		nonces:   make(map[common.Address]uint64),
		code:     make(map[common.Address][]byte),
		storage:  make(map[common.Address]map[common.Hash]common.Hash),
		logs:     make([]*types.Log, 0),
	}
}

func (m *MockStateDB) CreateAccount(addr common.Address) {}
func (m *MockStateDB) SubBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) {
	if bal, ok := m.balances[addr]; ok {
		bal.Sub(bal, amount)
	}
}
func (m *MockStateDB) AddBalance(addr common.Address, amount *uint256.Int, reason tracing.BalanceChangeReason) {
	if bal, ok := m.balances[addr]; ok {
		bal.Add(bal, amount)
	} else {
		m.balances[addr] = new(uint256.Int).Set(amount)
	}
}
func (m *MockStateDB) GetBalance(addr common.Address) *uint256.Int {
	if bal, ok := m.balances[addr]; ok {
		return bal
	}
	return uint256.NewInt(0)
}
func (m *MockStateDB) GetNonce(addr common.Address) uint64 {
	return m.nonces[addr]
}
func (m *MockStateDB) SetNonce(addr common.Address, nonce uint64) {
	m.nonces[addr] = nonce
}
func (m *MockStateDB) GetCodeHash(addr common.Address) common.Hash {
	return crypto.Keccak256Hash(m.code[addr])
}
func (m *MockStateDB) GetCode(addr common.Address) []byte {
	return m.code[addr]
}
func (m *MockStateDB) SetCode(addr common.Address, code []byte) {
	m.code[addr] = code
}
func (m *MockStateDB) GetCodeSize(addr common.Address) int {
	return len(m.code[addr])
}
func (m *MockStateDB) AddRefund(gas uint64) {
	m.refund += gas
}
func (m *MockStateDB) SubRefund(gas uint64) {
	m.refund -= gas
}
func (m *MockStateDB) GetRefund() uint64 {
	return m.refund
}
func (m *MockStateDB) GetCommittedState(addr common.Address, key common.Hash) common.Hash {
	return m.GetState(addr, key)
}
func (m *MockStateDB) GetState(addr common.Address, key common.Hash) common.Hash {
	if storage, ok := m.storage[addr]; ok {
		return storage[key]
	}
	return common.Hash{}
}
func (m *MockStateDB) SetState(addr common.Address, key common.Hash, value common.Hash) {
	if _, ok := m.storage[addr]; !ok {
		m.storage[addr] = make(map[common.Hash]common.Hash)
	}
	m.storage[addr][key] = value
}
func (m *MockStateDB) Suicide(addr common.Address) bool     { return true }
func (m *MockStateDB) HasSuicided(addr common.Address) bool { return false }
func (m *MockStateDB) Exist(addr common.Address) bool       { return true }
func (m *MockStateDB) Empty(addr common.Address) bool       { return false }
func (m *MockStateDB) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList) {
}
func (m *MockStateDB) AddressInAccessList(addr common.Address) bool { return true }
func (m *MockStateDB) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	return true, true
}
func (m *MockStateDB) AddAddressToAccessList(addr common.Address)                {}
func (m *MockStateDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {}
func (m *MockStateDB) RevertToSnapshot(revid int)                                {}
func (m *MockStateDB) Snapshot() int                                             { return 0 }
func (m *MockStateDB) AddLog(log *types.Log)                                     { m.logs = append(m.logs, log) }
func (m *MockStateDB) GetLogs(hash common.Hash, blockNumber uint64, hash2 common.Hash) []*types.Log {
	return m.logs
}
func (m *MockStateDB) Logs() []*types.Log                            { return m.logs }
func (m *MockStateDB) AddPreimage(hash common.Hash, preimage []byte) {}
func (m *MockStateDB) ForEachStorage(addr common.Address, cb func(key, value common.Hash) bool) error {
	return nil
}

// Implement StateDB interface extras
func (m *MockStateDB) CommitToDB(deleteEmptyObjects bool) (common.Hash, error) {
	return common.Hash{}, nil
}
func (m *MockStateDB) Finalise(deleteEmptyObjects bool) {}
func (m *MockStateDB) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return common.Hash{}
}
func (m *MockStateDB) SetTransientState(addr common.Address, key, value common.Hash) {}

func (m *MockStateDB) SelfDestruct(addr common.Address)                              {}
func (m *MockStateDB) HasSelfDestructed(addr common.Address) bool                    { return false }
func (m *MockStateDB) Selfdestruct6780(addr common.Address)                          {}
func (m *MockStateDB) CreateContract(addr common.Address)                            {}
func (m *MockStateDB) GetStorageRoot(addr common.Address) common.Hash                { return common.Hash{} }
func (m *MockStateDB) GetSelfDestruction(addr common.Address) bool                   { return false }
func (m *MockStateDB) Witness() *stateless.Witness                                   { return nil }
func (m *MockStateDB) AccessEvents() *state.AccessEvents                             { return nil }

type witness interface {
	Witness() *stateless.Witness
}

func main() {
	fmt.Println("🚀 Starting DEBUG EVM...")

	stateDB := NewMockStateDB()
	chainID := 1337

	// Manually construct ChainConfig to ensure Shanghai is ENABLED and Cancun is DISABLED
	zero := big.NewInt(0)
	zeroTime := uint64(0)
	chainConfig := &params.ChainConfig{
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
		MergeNetsplitBlock:            zero,
		TerminalTotalDifficulty:       big.NewInt(0),
		TerminalTotalDifficultyPassed: true,
		ShanghaiTime:                  &zeroTime, // ENABLED
		CancunTime:                    nil,       // DISABLED
	}

	executor := &evm.EVMExecutor{
		ChainConfig: chainConfig,
		VMConfig:    vm.Config{NoBaseFee: true},
	}

	fmt.Printf("DEBUG: Manual ChainConfig. ShanghaiTime: %d\n", *chainConfig.ShanghaiTime)

	caller := common.HexToAddress("0xf302B257cFFB7b30aF229F50F66315194d441C41")
	stateDB.AddBalance(caller, uint256.NewInt(1000000000000000000), tracing.BalanceChangeTransfer)

	// SimpleStorage Bytecode (compiled with solc 0.8.21+)
	bytecode := common.Hex2Bytes("6080604052348015600e575f5ffd5b50602a5f81905550610171806100235f395ff3fe608060405234801561000f575f5ffd5b506004361061003f575f3560e01c806360fe47b1146100435780636d4ce63c1461005f5780636d619daa1461007d575b5f5ffd5b61005d600480360381019061005891906100e8565b61009b565b005b6100676100a4565b6040516100749190610122565b60405180910390f35b6100856100ac565b6040516100929190610122565b60405180910390f35b805f8190555050565b5f5f54905090565b5f5481565b5f5ffd5b5f819050919050565b6100c7816100b5565b81146100d1575f5ffd5b50565b5f813590506100e2816100be565b92915050565b5f602082840312156100fd576100fc6100b1565b5b5f61010a848285016100d4565b91505092915050565b61011c816100b5565b82525050565b5f6020820190506101355f830184610113565b9291505056fea264697066735822122083f338daec5661be656aaec9f2df261368bb077ce6a7a9b774db6a177c7697a264736f6c63430008210033")
	value := big.NewInt(0)
	gasLimit := uint64(30000000)

	res, err := executor.DeployContract(stateDB, caller, bytecode, value, gasLimit)
	if err != nil {
		fmt.Printf("❌ Deployment failed: %v\n", err)
	} else {
		fmt.Printf("✅ Deployment success! Address: %s, GasUsed: %d\n", res.ContractAddr.Hex(), res.GasUsed)
	}
}
