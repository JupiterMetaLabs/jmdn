# Security Module

## Overview

The Security module provides security validation for the JMZK network. It implements three-layer security checks for transactions and blocks: DID validation, signature verification, and balance checking.

## Purpose

The Security module enables:
- Transaction security validation
- Block security validation
- DID existence checking
- Signature verification
- Balance validation
- Chain ID validation

## Key Components

### 1. Three-Layer Security Checks
**File:** `Security.go`

Main security validation:
- `ThreeChecks`: Three-layer security check for transactions
- `CheckZKBlockValidation`: Security validation for ZK blocks
- `CheckAddressExist`: Check if DID/address exists
- `CheckSignature`: Verify transaction signature
- `CheckBalance`: Validate account balance

### 2. Chain ID Management

Chain ID validation:
- `SetExpectedChainID`: Set expected chain ID
- `SetExpectedChainIDBig`: Set expected chain ID from big.Int
- Chain ID validation in transactions

## Key Functions

### Three-Layer Security Check

```go
// Three-layer security check for transactions
func ThreeChecks(tx *config.Transaction) (bool, error) {
    // Check 1: DID/Address existence
    // Check 2: Signature verification
    // Check 3: Balance validation
}
```

### Check ZK Block Validation

```go
// Security validation for ZK blocks
func CheckZKBlockValidation(zkBlock *config.ZKBlock) (bool, error) {
    // Validate all transactions in block
    // Validate block hash
}
```

### Check Address Existence

```go
// Check if DID/address exists
func CheckAddressExist(tx *config.Transaction, conn *config.PooledConnection) (bool, error) {
    // Check sender address exists
    // Check receiver address exists
}
```

### Check Signature

```go
// Verify transaction signature
func CheckSignature(tx *config.Transaction) (bool, error) {
    // Recover public key from signature
    // Verify signature matches transaction
}
```

### Check Balance

```go
// Validate account balance
func CheckBalance(tx *config.Transaction, conn *config.PooledConnection) (bool, error) {
    // Check sender has sufficient balance
    // Check balance >= value + gas
}
```

## Usage

### Set Chain ID

```go
import "gossipnode/Security"

// Set expected chain ID
Security.SetExpectedChainID(7000700)
```

### Validate Transaction

```go
// Validate transaction
valid, err := Security.ThreeChecks(tx)
if err != nil {
    log.Error(err)
}
if !valid {
    log.Error("Transaction validation failed")
}
```

### Validate Block

```go
// Validate ZK block
valid, err := Security.CheckZKBlockValidation(block)
if err != nil {
    log.Error(err)
}
if !valid {
    log.Error("Block validation failed")
}
```

## Integration Points

### Block Module
- Uses security validation for blocks
- Validates transactions before processing

### Transaction Processing
- Uses security validation for transactions
- Validates before submission to mempool

### Database (DB_OPs)
- Queries account information
- Validates DID existence
- Checks account balances

### Config Module
- Uses chain ID configuration
- Accesses transaction structures

## Configuration

Chain ID can be configured via command-line flag:

```bash
./jmdn -chainID 7000700  # Default chain ID
```

## Error Handling

The module includes comprehensive error handling:
- Invalid transaction errors
- Missing DID errors
- Invalid signature errors
- Insufficient balance errors
- Chain ID mismatch errors

## Logging

Security operations are logged to:
- `logs/SecurityModule.log`: Security operations
- Loki (if enabled): Centralized logging

## Security

- Three-layer security validation
- Signature verification
- Balance checking
- Chain ID validation
- Input sanitization

## Performance

- Efficient validation checks
- Database connection pooling
- Optimized signature verification
- Fast balance queries

## Testing

Test files:
- `Security_test.go`: Security operation tests
- Integration tests
- Validation tests

## Future Enhancements

- Enhanced validation rules
- Improved error handling
- Better performance
- Additional security checks
- Advanced signature verification

