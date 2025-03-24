package SIM

import (
    "encoding/json"
    "fmt"
    "math/big"
    "math/rand"
    "os"
    "sync"
    "time"

    // "github.com/ethereum/go-ethereum/common"
    "github.com/libp2p/go-libp2p/core/host"
    "github.com/rs/zerolog/log"

    "gossipnode/Block"
    "gossipnode/messaging"
)

// Default account file path
const DefaultAccountPath = "Account.json"

// EthTransaction represents a structured Ethereum transaction for simulation
type EthTransaction struct {
    TxType            string   `json:"tx_type"`            // "legacy" or "eip1559"
    TxHash            string   `json:"tx_hash"`            // Transaction hash
    From              string   `json:"from"`               // Sender address
    To                string   `json:"to"`                 // Recipient address
    Value             string   `json:"value"`              // Transaction amount
    Nonce             uint64   `json:"nonce"`              // Transaction nonce
    GasLimit          uint64   `json:"gas_limit"`          // Gas limit
    GasPrice          string   `json:"gas_price,omitempty"`          // Gas price (legacy)
    MaxPriorityFee    string   `json:"max_priority_fee,omitempty"`   // Max priority fee (EIP-1559)
    MaxFee            string   `json:"max_fee,omitempty"`            // Max fee (EIP-1559)
    Data              string   `json:"data,omitempty"`               // Transaction data
    ChainID           int64    `json:"chain_id"`           // Chain ID
    BlockHeight       uint64   `json:"block_height"`       // Block height (simulated)
    Timestamp         int64    `json:"timestamp"`          // Transaction timestamp
}

// EthSimulator manages Ethereum transaction simulation
type EthSimulator struct {
    accountPath       string
    chainID           int64
    minNonce          uint64
    currentNonce      uint64
    blockHeight       uint64
    currentRecipients []string
    recipientMutex    sync.Mutex
    accountInfo       *AccountInfo
    mutex             sync.Mutex
}

// AccountInfo matches the structure in Block package
type AccountInfo struct {
    DID       string `json:"did"`
    Mnemonic  string `json:"mnemonic"`
    PublicKey string `json:"public_key"`
    Address   string `json:"address"`
}

// NewEthSimulator creates a new Ethereum transaction simulator
func NewEthSimulator(accountPath string, chainID int64) (*EthSimulator, error) {
    if accountPath == "" {
        accountPath = DefaultAccountPath
    }

    // If account file doesn't exist, create a sample one
    if _, err := os.Stat(accountPath); os.IsNotExist(err) {
        log.Warn().Msg("Account file not found, creating example file. Replace with real wallet for production.")
        err = createSampleAccountFile(accountPath)
        if err != nil {
            return nil, fmt.Errorf("failed to create sample account file: %w", err)
        }
    }

    // Load account info
    accountInfo, err := loadAccountInfo(accountPath)
    if err != nil {
        return nil, fmt.Errorf("failed to load account info: %w", err)
    }

    // Create sample recipients
    recipients := []string{
        "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
        "0x8626f6940E2eb28930eFb4CeF49B2d1F2C9C1199",
        "0xdD2FD4581271e230360230F9337D5c0430Bf44C0",
        "0xbDA5747bFD65F08deb54cb465eB87D40e51B197E",
        "0x2546BcD3c84621e976D8185a91A922aE77ECEc30",
    }

    return &EthSimulator{
        accountPath:       accountPath,
        chainID:           chainID,
        minNonce:          0,
        currentNonce:      0,
        blockHeight:       1,
        currentRecipients: recipients,
        accountInfo:       accountInfo,
    }, nil
}

// loadAccountInfo loads account information from file
func loadAccountInfo(filePath string) (*AccountInfo, error) {
    data, err := os.ReadFile(filePath)
    if err != nil {
        return nil, fmt.Errorf("error reading account file: %w", err)
    }

    var account AccountInfo
    if err := json.Unmarshal(data, &account); err != nil {
        return nil, fmt.Errorf("error parsing account data: %w", err)
    }

    return &account, nil
}

// createSampleAccountFile creates a sample account file for testing
func createSampleAccountFile(filePath string) error {
    // Example mnemonic - DO NOT USE IN PRODUCTION
    sampleAccount := AccountInfo{
        DID:       "did:example:123456789abcdefghi",
        Mnemonic:  "test test test test test test test test test test test junk",
        PublicKey: "0x04a12a9a9be3c7d9944c92ba874998edfe93e8209ac1742df1b2b4333e7926ad0d5f86987b3c715c1d03976eda7b17f18d54c68e2ca7281c3846612b98b3bf8e94",
        Address:   "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
    }

    data, err := json.MarshalIndent(sampleAccount, "", "  ")
    if err != nil {
        return err
    }

    return os.WriteFile(filePath, data, 0644)
}

// randomRecipient returns a random recipient address
func (es *EthSimulator) randomRecipient() string {
    es.recipientMutex.Lock()
    defer es.recipientMutex.Unlock()

    if len(es.currentRecipients) == 0 {
        return "0x742d35Cc6634C0532925a3b844Bc454e4438f44e" // Fallback
    }
    return es.currentRecipients[rand.Intn(len(es.currentRecipients))]
}

// randomAmount generates a random transaction amount
func randomAmount() *big.Int {
    // Generate random amount between 0.001 and 1.0 ETH (in wei)
    min := new(big.Int).Mul(big.NewInt(1000000000000000), big.NewInt(1))     // 0.001 ETH
    max := new(big.Int).Mul(big.NewInt(1000000000000000000), big.NewInt(1))  // 1 ETH
    
    diff := new(big.Int).Sub(max, min)
    randVal := new(big.Int).Rand(rand.New(rand.NewSource(time.Now().UnixNano())), diff)
    return new(big.Int).Add(min, randVal)
}

// randomGasPrice generates a random gas price
func randomGasPrice() *big.Int {
    // Generate random gas price between 1 and 100 Gwei
    min := new(big.Int).Mul(big.NewInt(1000000000), big.NewInt(1))  // 1 Gwei
    max := new(big.Int).Mul(big.NewInt(1000000000), big.NewInt(100)) // 100 Gwei
    
    diff := new(big.Int).Sub(max, min)
    randVal := new(big.Int).Rand(rand.New(rand.NewSource(time.Now().UnixNano())), diff)
    return new(big.Int).Add(min, randVal)
}

// randomMaxPriorityFee generates a random max priority fee
func randomMaxPriorityFee() *big.Int {
    // Generate random priority fee between 0.1 and 5 Gwei
    min := new(big.Int).Mul(big.NewInt(100000000), big.NewInt(1))  // 0.1 Gwei
    max := new(big.Int).Mul(big.NewInt(1000000000), big.NewInt(5)) // 5 Gwei
    
    diff := new(big.Int).Sub(max, min)
    randVal := new(big.Int).Rand(rand.New(rand.NewSource(time.Now().UnixNano())), diff)
    return new(big.Int).Add(min, randVal)
}

// randomMaxFee generates a random max fee based on priority fee
func randomMaxFee(priorityFee *big.Int) *big.Int {
    // Generate random base fee between 1 and 20 Gwei
    min := new(big.Int).Mul(big.NewInt(1000000000), big.NewInt(1))  // 1 Gwei
    max := new(big.Int).Mul(big.NewInt(1000000000), big.NewInt(20)) // 20 Gwei
    
    diff := new(big.Int).Sub(max, min)
    baseFee := new(big.Int).Rand(rand.New(rand.NewSource(time.Now().UnixNano())), diff)
    baseFee.Add(baseFee, min)
    
    // Max fee = priority fee + base fee
    return new(big.Int).Add(priorityFee, baseFee)
}

// getNextNonce gets the next nonce for transactions
func (es *EthSimulator) getNextNonce() uint64 {
    es.mutex.Lock()
    defer es.mutex.Unlock()
    
    nonce := es.currentNonce
    es.currentNonce++
    return nonce
}

// CreateRandomTransaction generates a random Ethereum transaction
func (es *EthSimulator) CreateRandomTransaction() (*EthTransaction, error) {
    // Randomly choose between legacy and EIP-1559 transactions
    isEIP1559 := rand.Intn(2) == 1

    // Generate random transaction parameters
    nonce := es.getNextNonce()
    recipient := es.randomRecipient()
    amount := randomAmount()
    gasLimit := uint64(21000) // Standard ETH transfer

    var txHash string


    if isEIP1559 {
        // Create EIP-1559 transaction
        maxPriorityFee := randomMaxPriorityFee()
        maxFee := randomMaxFee(maxPriorityFee)
        
        tx, err := Block.GenerateEIP1559Transaction(
            es.accountPath,
            big.NewInt(es.chainID),
            recipient,
            amount,
            nonce,
            gasLimit,
            maxFee,
            maxPriorityFee,
            []byte{}, // Empty data
            Block.AccessList{}, // Empty access list
        )
        
        if err != nil {
            return nil, fmt.Errorf("failed to generate EIP-1559 transaction: %w", err)
        }
        
        txHash, err = Block.Hash(tx)
        if err != nil {
            return nil, fmt.Errorf("failed to hash EIP-1559 transaction: %w", err)
        }
        
        return &EthTransaction{
            TxType:         "eip1559",
            TxHash:         txHash,
            From:           es.accountInfo.Address,
            To:             recipient,
            Value:          amount.String(),
            Nonce:          nonce,
            GasLimit:       gasLimit,
            MaxPriorityFee: maxPriorityFee.String(),
            MaxFee:         maxFee.String(),
            ChainID:        es.chainID,
            BlockHeight:    es.blockHeight,
            Timestamp:      time.Now().Unix(),
        }, nil
    } else {
        // Create legacy transaction
        gasPrice := randomGasPrice()
        
        tx, err := Block.GenerateLegacyTransaction(
            es.accountPath,
            big.NewInt(es.chainID),
            recipient,
            amount,
            nonce,
            gasLimit,
            gasPrice,
            []byte{}, // Empty data
        )
        
        if err != nil {
            return nil, fmt.Errorf("failed to generate legacy transaction: %w", err)
        }
        
        txHash, err = Block.Hash(tx)
        if err != nil {
            return nil, fmt.Errorf("failed to hash legacy transaction: %w", err)
        }
        
        return &EthTransaction{
            TxType:      "legacy",
            TxHash:      txHash,
            From:        es.accountInfo.Address,
            To:          recipient,
            Value:       amount.String(),
            Nonce:       nonce,
            GasLimit:    gasLimit,
            GasPrice:    gasPrice.String(),
            ChainID:     es.chainID,
            BlockHeight: es.blockHeight,
            Timestamp:   time.Now().Unix(),
        }, nil
    }
}

// PropagateRandomTransaction creates and propagates a random Ethereum transaction
func (es *EthSimulator) PropagateRandomTransaction(h host.Host) error {
    // Create a random transaction
    tx, err := es.CreateRandomTransaction()
    if err != nil {
        return fmt.Errorf("failed to create transaction: %w", err)
    }
    
    // Log the transaction details
    log.Info().
        Str("tx_hash", tx.TxHash).
        Str("tx_type", tx.TxType).
        Str("from", tx.From).
        Str("to", tx.To).
        Str("value", tx.Value).
        Uint64("nonce", tx.Nonce).
        Msg("Propagating Ethereum transaction")
    
    // Convert transaction to map for propagation
    txData := map[string]string{
        "transaction_id": tx.TxHash,
        "type":           tx.TxType,
        "from":           tx.From,
        "to":             tx.To,
        "value":          tx.Value,
        "nonce":          fmt.Sprintf("%d", tx.Nonce),
        "gas_limit":      fmt.Sprintf("%d", tx.GasLimit),
        "chain_id":       fmt.Sprintf("%d", tx.ChainID),
        "block_height":   fmt.Sprintf("%d", tx.BlockHeight),
        "timestamp":      fmt.Sprintf("%d", tx.Timestamp),
    }
    
    // Add type-specific fields
    if tx.TxType == "legacy" {
        txData["gas_price"] = tx.GasPrice
    } else {
        txData["max_priority_fee"] = tx.MaxPriorityFee
        txData["max_fee"] = tx.MaxFee
    }
    
    // Propagate the transaction
    err = messaging.PropagateBlock(h, txData)
    if err != nil {
        return fmt.Errorf("failed to propagate transaction: %w", err)
    }
    
    // Pretty print transaction details
    fmt.Printf("\nTransaction propagated: %s\n", tx.TxHash)
    fmt.Printf("Type: %s\n", tx.TxType)
    fmt.Printf("From: %s\n", tx.From)
    fmt.Printf("To: %s\n", tx.To)
    fmt.Printf("Value: %s wei\n", tx.Value)
    
    // Increment block height for next transaction (simulating blocks)
    es.mutex.Lock()
    es.blockHeight++
    es.mutex.Unlock()
    
    return nil
}

// StartEthSimulation starts Ethereum transaction simulation at random intervals
func StartEthSimulation(h host.Host, chainID int64, minInterval, maxInterval time.Duration) error {
    // Initialize simulator
    simulator, err := NewEthSimulator(DefaultAccountPath, chainID)
    if err != nil {
        return fmt.Errorf("failed to initialize Ethereum simulator: %w", err)
    }
    
    // Seed the random number generator
    rand.Seed(time.Now().UnixNano())
    
    log.Info().
        Int64("chain_id", chainID).
        Dur("min_interval", minInterval).
        Dur("max_interval", maxInterval).
        Msg("Starting Ethereum transaction simulation")
    
    go func() {
        for {
            // Wait for a random interval
            interval := minInterval + time.Duration(rand.Int63n(int64(maxInterval-minInterval)))
            time.Sleep(interval)
            
            // Generate and propagate a random transaction
            err := simulator.PropagateRandomTransaction(h)
            if err != nil {
                log.Error().Err(err).Msg("Ethereum transaction simulation error")
            }
        }
    }()
    
    return nil
}

// RunOneTimeEthSimulation runs a fixed number of Ethereum transaction propagations
func RunOneTimeEthSimulation(h host.Host, chainID int64, count int) error {
    // Initialize simulator
    simulator, err := NewEthSimulator(DefaultAccountPath, chainID)
    if err != nil {
        return fmt.Errorf("failed to initialize Ethereum simulator: %w", err)
    }
    
    log.Info().
        Int("count", count).
        Int64("chain_id", chainID).
        Msg("Running one-time Ethereum transaction simulation")
    
    for i := 0; i < count; i++ {
        fmt.Printf("\nPropagating transaction %d of %d\n", i+1, count)
        
        err := simulator.PropagateRandomTransaction(h)
        if err != nil {
            return fmt.Errorf("failed to propagate transaction %d: %w", i+1, err)
        }
        
        // Small delay between propagations to avoid overwhelming the network
        time.Sleep(500 * time.Millisecond)
    }
    
    return nil
}

// SimulateEthNetworkStress simulates high load on the network with Ethereum transactions
func SimulateEthNetworkStress(h host.Host, chainID int64, txPerSecond, durationSeconds int) error {
    // Initialize simulator
    simulator, err := NewEthSimulator(DefaultAccountPath, chainID)
    if err != nil {
        return fmt.Errorf("failed to initialize Ethereum simulator: %w", err)
    }
    
    log.Info().
        Int("transactions_per_second", txPerSecond).
        Int("duration_seconds", durationSeconds).
        Int64("chain_id", chainID).
        Msg("Starting Ethereum network stress test")
    
    // Calculate delay between transactions
    delay := time.Second / time.Duration(txPerSecond)
    totalTx := txPerSecond * durationSeconds
    
    fmt.Printf("Starting Ethereum stress test: %d transactions over %d seconds\n", 
        totalTx, durationSeconds)
    
    // Track start time for progress updates
    startTime := time.Now()
    endTime := startTime.Add(time.Duration(durationSeconds) * time.Second)
    
    // Launch transactions at the specified rate
    for i := 0; i < totalTx; i++ {
        // Check if we've exceeded the duration
        if time.Now().After(endTime) {
            break
        }
        
        // Show periodic progress
        if i%txPerSecond == 0 && i > 0 {
            elapsed := time.Since(startTime).Seconds()
            fmt.Printf("Progress: %d transactions sent (%.1f seconds elapsed)\n", 
                i, elapsed)
        }
        
        // Generate and propagate transaction
        err := simulator.PropagateRandomTransaction(h)
        if err != nil {
            log.Error().Err(err).Int("tx_num", i).Msg("Stress test transaction error")
        }
        
        // Wait for the calculated delay
        time.Sleep(delay)
    }
    
    // Report completion
    actualDuration := time.Since(startTime)
    fmt.Printf("\nEthereum stress test completed: %d transactions sent in %.1f seconds\n", 
        totalTx, actualDuration.Seconds())
    
    return nil
}