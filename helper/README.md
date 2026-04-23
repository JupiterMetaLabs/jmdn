# Helper Module

## Overview

The Helper module provides utility functions and helper methods used throughout the JMDT network. It includes data type conversions, network utilities, and broadcast handling.

## Purpose

The Helper module provides:
- Data type conversion utilities
- Network interface utilities (Yggdrasil)
- Broadcast notification handling
- JSON serialization helpers
- Number conversion utilities

## Key Components

### 1. Data Conversion
**File:** `helper.go`

Data type conversion utilities:
- `ConvertBigToUint256`: Convert big.Int to uint256
- `BigIntToUint64Safe`: Convert big.Int to uint64 with overflow checking
- `Uint64ToBytes`: Convert uint64 to byte array

### 2. Network Utilities
**File:** `tun_ip.go`

Network interface utilities:
- `GetTun0GlobalIPv6`: Get Yggdrasil global IPv6 address
- `scanAllInterfacesForYggdrasil`: Scan all interfaces for Yggdrasil addresses

### 3. Broadcast Handling
**File:** `helper.go`

Broadcast notification handling:
- `SetBroadcastHandler`: Set broadcast handler
- `NotifyBroadcast`: Send broadcast notification

### 4. JSON Utilities
**File:** `helper.go`

JSON serialization helpers:
- `ToJSON`: Convert value to JSON bytes

## Key Functions

### Convert BigInt to Uint256

```go
// Convert big.Int to uint256 with overflow checking
func ConvertBigToUint256(b *big.Int) (*uint256.Int, bool) {
    u, overflow := uint256.FromBig(b)
    if overflow {
        log.Error().Msg("Overflow occurred")
        return nil, true
    }
    return u, overflow
}
```

### Convert BigInt to Uint64

```go
// Convert big.Int to uint64 with safety checks
func BigIntToUint64Safe(b *big.Int) (uint64, error) {
    if b.Sign() < 0 {
        return 0, fmt.Errorf("cannot convert negative big.Int to uint64")
    }
    if b.BitLen() > 64 {
        return 0, fmt.Errorf("big.Int too large for uint64")
    }
    return b.Uint64(), nil
}
```

### Get Yggdrasil IPv6 Address

```go
// Get Yggdrasil global IPv6 address
func GetTun0GlobalIPv6() (string, error) {
    // Tries common interface names: utun6, tun0, utun0, ygg0, yggdrasil
    // Scans all interfaces if common names fail
    // Returns global IPv6 address
}
```

### Broadcast Notification

```go
// Send broadcast notification
func NotifyBroadcast(msg config.BlockMessage) {
    if broadcastHandler == nil {
        return
    }
    // Marshal to JSON
    // Send to broadcast handler
}
```

## Usage

### Data Conversion

```go
import "gossipnode/helper"

// Convert big.Int to uint256
bigVal := big.NewInt(123456789)
u256, overflow := helper.ConvertBigToUint256(bigVal)
if overflow {
    log.Error().Msg("Overflow occurred")
}

// Convert big.Int to uint64
val, err := helper.BigIntToUint64Safe(bigVal)
if err != nil {
    log.Error().Err(err).Msg("Conversion failed")
}

// Convert uint64 to bytes
bytes := helper.Uint64ToBytes(123456789)
```

### Network Utilities

```go
import "gossipnode/helper"

// Get Yggdrasil IPv6 address
ipv6, err := helper.GetTun0GlobalIPv6()
if err != nil {
    log.Error().Err(err).Msg("Failed to get Yggdrasil address")
} else {
    fmt.Printf("Yggdrasil IPv6: %s\n", ipv6)
}
```

### Broadcast Handling

```go
import "gossipnode/helper"

// Set broadcast handler
handler := &MyBroadcastHandler{}
helper.SetBroadcastHandler(handler)

// Send broadcast notification
msg := config.BlockMessage{
    Type: "new_block",
    ID:   "block_123",
    // ... block data
}
helper.NotifyBroadcast(msg)
```

### JSON Utilities

```go
import "gossipnode/helper"

// Convert value to JSON
data := map[string]interface{}{
    "key": "value",
    "number": 123,
}
jsonBytes := helper.ToJSON(data)
```

## Integration Points

### Config Module
- Uses block message structures
- Accesses configuration constants

### Node Module
- Uses network utilities for Yggdrasil
- Accesses network interfaces

### Messaging Module
- Uses broadcast handling
- Sends notifications

### Block Module
- Uses data conversion utilities
- Converts block data

## Testing

Test files:
- `helper_test.go`: Helper function tests
- Data conversion tests
- Network utility tests

## Error Handling

The module includes comprehensive error handling:
- Overflow detection for number conversions
- Negative number checks
- Network interface errors
- JSON serialization errors

## Performance

- Efficient number conversions
- Fast JSON serialization
- Optimized network interface scanning

## Future Enhancements

- Additional data type conversions
- Enhanced network utilities
- Improved error handling
- Performance optimizations

