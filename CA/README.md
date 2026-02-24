# CA (Certificate Authority) Module

## Overview

The CA module provides certificate authority functionality for the JMDT network. It generates and manages TLS certificates for ImmuDB, ensuring secure communication.

## Purpose

The CA module enables:
- Certificate Authority (CA) generation
- Server certificate generation
- TLS certificate management
- Certificate expiration checking
- Automatic certificate regeneration

## Key Components

### 1. ImmuDB CA
**File:** `ImmuDB_CA/ImmuDB_CA.go`

Main CA implementation:
- `EnsureTLSAssets`: Ensure TLS certificates exist and are valid
- `loadOrCreateCA`: Load or create CA certificate
- `loadOrCreateServerCert`: Load or create server certificate
- `generateCA`: Generate new CA certificate
- `generateServerCert`: Generate new server certificate

### 2. Certificate Management

Certificate operations:
- `readPEMFiles`: Read PEM certificate files
- `parseCA`: Parse CA certificate
- `parseCert`: Parse server certificate
- `writePEM`: Write PEM certificate file

## Key Functions

### Ensure TLS Assets

```go
// Ensure TLS certificates exist and are valid
func EnsureTLSAssets(dir string) error {
    // Check if CA exists and is valid
    // Check if server cert exists and is valid
    // Generate if missing or expired
}
```

### Generate CA

```go
// Generate new CA certificate
func generateCA(certPath, keyPath string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
    // Generate ECDSA P256 key
    // Create CA certificate
    // Write certificate files
}
```

### Generate Server Certificate

```go
// Generate new server certificate
func generateServerCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, certPath, keyPath string, now time.Time) error {
    // Generate server key
    // Create server certificate signed by CA
    // Write certificate files
}
```

## Usage

### Ensure TLS Assets

```go
import "gossipnode/CA/ImmuDB_CA"

// Ensure TLS certificates exist
err := ImmuDB_CA.EnsureTLSAssets(".immudb_state")
if err != nil {
    log.Fatal(err)
}
```

### Certificate Files

The module generates the following certificate files:
- `ca.key.pem`: CA private key
- `ca.cert.pem`: CA certificate
- `server.key.pem`: Server private key
- `server.cert.pem`: Server certificate

## Integration Points

### ImmuDB
- Provides TLS certificates for ImmuDB
- Ensures secure ImmuDB connections

### Config Module
- Uses certificate paths
- Accesses configuration constants

## Configuration

Certificate configuration:
- Certificate directory (default: `.immudb_state`)
- Certificate validity period (default: 365 days)
- Certificate key type (ECDSA P256)

## Error Handling

The module includes comprehensive error handling:
- Certificate generation errors
- File read/write errors
- Certificate parsing errors
- Expiration checking errors

## Logging

CA operations are logged to:
- Application logs
- Certificate generation logs
- Error logs

## Security

- Secure certificate generation
- Private key protection
- Certificate expiration checking
- Automatic certificate renewal

## Performance

- Efficient certificate generation
- Fast certificate validation
- Optimized file operations

## Testing

Test files:
- `CA_test.go`: CA operation tests
- Certificate generation tests
- Validation tests

## Future Enhancements

- Enhanced certificate management
- Improved expiration handling
- Better error recovery
- Performance optimizations
- Additional certificate types

