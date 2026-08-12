# Thebe Debug URLs

This document lists the HTTP debug routes exposed by the Facade server for Thebe/Cassata projection reads.

## Base URL

- Default: `http://10.50.0.3:6545`
- Route prefix: `/debug/thebe`

If your host/port differs, replace `10.50.0.3:6545` with your configured Facade endpoint.

## Browser-Friendly URLs

Open these directly in your browser.

### Health / status

- [Status](http://10.50.0.3:6545/debug/thebe/status)
  - Confirms whether Thebe read API is enabled on the current node.

### Accounts

- [List accounts (paged)](http://10.50.0.3:6545/debug/thebe/accounts?limit=50&offset=0)
- Account by address: `http://10.50.0.3:6545/debug/thebe/accounts/{address}`
- Account nonce: `http://10.50.0.3:6545/debug/thebe/accounts/{address}/nonce`
- Account transactions (paged): `http://10.50.0.3:6545/debug/thebe/accounts/{address}/transactions?limit=50&offset=0`

### Blocks

- [List blocks (paged)](http://10.50.0.3:6545/debug/thebe/blocks?limit=50&offset=0)
- Block by number: `http://10.50.0.3:6545/debug/thebe/blocks/{num}`
- Block transactions: `http://10.50.0.3:6545/debug/thebe/blocks/{num}/transactions`
- Block zkproof: `http://10.50.0.3:6545/debug/thebe/blocks/{num}/zkproof`
- Block snapshot: `http://10.50.0.3:6545/debug/thebe/blocks/{num}/snapshot`

### Transactions and snapshots

- Transaction by hash: `http://10.50.0.3:6545/debug/thebe/transactions/{txHash}`
- [List snapshots (paged)](http://10.50.0.3:6545/debug/thebe/snapshots?limit=50&offset=0)

## Path Parameters

- `{address}`: hex account address (with or without `0x`)
- `{num}`: block number (uint64)
- `{txHash}`: 32-byte tx hash (with or without `0x`)

## Quick examples

```bash
curl "http://10.50.0.3:6545/debug/thebe/status"
curl "http://10.50.0.3:6545/debug/thebe/accounts?limit=25&offset=0"
curl "http://10.50.0.3:6545/debug/thebe/accounts/0x0123456789abcdef0123456789abcdef01234567/nonce"
curl "http://10.50.0.3:6545/debug/thebe/blocks/100/transactions"
curl "http://10.50.0.3:6545/debug/thebe/transactions/0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
```

## Notes

- These are debug/read routes intended for operator use.
- When Thebe is disabled, `/debug/thebe/status` still responds, while other endpoints return `503`.
- Addresses and transaction hashes should be hex-formatted (with or without `0x`).
