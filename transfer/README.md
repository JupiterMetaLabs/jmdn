# Transfer Module

## Overview

The Transfer module provides file transfer capabilities for the JMZK network. It enables efficient peer-to-peer file transfers with adaptive buffer sizing for optimal performance.

## Purpose

The Transfer module enables:
- Peer-to-peer file transfers
- Adaptive buffer sizing for optimal performance
- File transfer metrics and monitoring
- Large file handling
- Stream-based file transfer

## Key Components

### 1. File Transfer
**File:** `file.go`

Main file transfer implementation:
- `SendFile`: Send file to peer
- `HandleFileStream`: Handle incoming file stream
- `getOptimalBufferSize`: Get optimal buffer size for peer
- `adaptBufferSize`: Adapt buffer size based on performance
- `calculateSpeedTrend`: Calculate speed trend for adaptation

### 2. Adaptive Buffer Management

Adaptive buffer sizing:
- Initial buffer size based on peer history
- Dynamic buffer adjustment based on transfer speed
- Speed trend calculation
- Performance-based optimization

## Key Functions

### Send File

```go
// Send file to peer
func SendFile(h host.Host, peerID peer.ID, filePath, remotePath string) error {
    // Open file
    // Create stream
    // Send file metadata
    // Transfer file with adaptive buffering
    // Record metrics
}
```

### Handle File Stream

```go
// Handle incoming file stream
func HandleFileStream(s network.Stream, outputPath string) {
    // Read file metadata
    // Create output file
    // Receive file with adaptive buffering
    // Record metrics
}
```

### Adaptive Buffer Sizing

```go
// Adapt buffer size based on performance
func adaptBufferSize(peerID string, currentSize int, speed float64, trend float64) int {
    // Calculate optimal buffer size
    // Adjust based on speed and trend
    // Return new buffer size
}
```

## Usage

### Send File

```go
import "gossipnode/transfer"

// Send file to peer
err := transfer.SendFile(host, peerID, "/path/to/file", "remote_filename")
if err != nil {
    log.Error(err)
}
```

### Handle File Stream

```go
// Set up file stream handler
host.SetStreamHandler(config.FileProtocol, func(s network.Stream) {
    transfer.HandleFileStream(s, "/output/path")
})
```

### Via CLI

```bash
# Send file via CLI
./jmdn -cmd sendfile /ip4/192.168.1.100/tcp/15000/p2p/QmPeerID /path/to/file remote_filename
```

## Integration Points

### Node Module
- Uses libp2p host for file transfer
- Sets up stream handlers

### Metrics Module
- Records file transfer metrics
- Tracks transfer performance

### Config Module
- Uses file protocol ID
- Accesses configuration constants

## Configuration

File transfer configuration:
- `FileProtocol`: File transfer protocol ID
- Buffer size limits
- Speed measurement intervals

## Error Handling

The module includes comprehensive error handling:
- File open errors
- Stream creation errors
- Transfer errors
- Buffer size errors

## Logging

File transfer operations are logged to:
- Application logs
- Transfer progress logs
- Error logs

## Security

- File path validation
- Stream security
- File size limits
- Input sanitization

## Performance

- **Adaptive Buffering**: Optimizes buffer size based on transfer speed
- **Concurrent Transfers**: Supports multiple concurrent transfers
- **Efficient Streaming**: Stream-based transfer for large files
- **Performance Monitoring**: Tracks transfer speeds and adjusts accordingly

## Metrics

File transfer metrics:
- `FileTransferBytesCounter`: Bytes transferred
- `FileTransferDuration`: Transfer duration histogram
- `FileTransferSpeedMBPS`: Transfer speed histogram

## Testing

Test files:
- `transfer_test.go`: File transfer tests
- Integration tests
- Performance tests

## Future Enhancements

- Enhanced buffer management
- Improved error recovery
- Better performance optimization
- Additional transfer protocols
- Compression support

