# Foundry Quickstart — JMDT Node

This guide shows how to connect [Foundry](https://getfoundry.sh) to a local or remote JMDT node.

---

## 1. Install Foundry

```bash
curl -L https://foundry.paradigm.xyz | bash
foundryup
```

Verify installation:

```bash
forge --version
cast --version
```

---

## 2. Configure Your Project

Copy the example config into your Foundry project root:

```bash
cp foundry.toml.example foundry.toml
```

Edit `foundry.toml` to set your node address and chain ID:

```toml
[profile.jmdt]
rpc_url = "http://<YOUR_NODE_IP>:8545"
chain_id = <CHAIN_ID>   # fetch with: cast chain-id --rpc-url http://<YOUR_NODE_IP>:8545
```

---

## 3. Deploy a Contract

### Using `forge create`

```bash
forge create \
  --rpc-url http://localhost:8545 \
  --private-key <DEPLOYER_PRIVATE_KEY> \
  src/MyContract.sol:MyContract
```

For a constructor argument:

```bash
forge create \
  --rpc-url http://localhost:8545 \
  --private-key <DEPLOYER_PRIVATE_KEY> \
  src/MyContract.sol:MyContract \
  --constructor-args "Hello JMDT"
```

---

## 4. Run a Deployment Script

```bash
forge script script/Deploy.s.sol:DeployScript \
  --rpc-url http://localhost:8545 \
  --private-key <DEPLOYER_PRIVATE_KEY> \
  --broadcast
```

> **Tip:** Add `--no-storage-caching` if you need accurate storage reads during  
> the trace (JMDT does not yet support historical state snapshots):
>
> ```bash
> forge script ... --broadcast --no-storage-caching
> ```

---

## 5. Verify a Deployment

After deployment, note the transaction hash from the output. Then query the receipt:

```bash
cast receipt <TX_HASH> --rpc-url http://localhost:8545
```

Or call via `curl`:

```bash
curl -s -X POST http://localhost:8545 \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc":"2.0",
    "method":"eth_getTransactionReceipt",
    "params":["<TX_HASH>"],
    "id":1
  }' | jq .
```

A successful deployment returns `"status": "0x1"` and a non-null `"contractAddress"`.

---

## 6. Trace a Transaction (Debugging)

```bash
curl -s -X POST http://localhost:8545 \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc":"2.0",
    "method":"debug_traceTransaction",
    "params":["<TX_HASH>"],
    "id":1
  }' | jq '.result.structLogs | length'
```

> **Note:** `debug_traceTransaction` on JMDT re-executes the call against the  
> *current* state (best-effort). Historical pre-state tracing is a planned  
> Phase 5 feature. View calls are always accurate; storage-mutating replays  
> may differ if state has changed since the transaction was mined.

---

## 7. Add the Network to MetaMask

1. Open MetaMask → Settings → Networks → Add a network manually.
2. Use the values from `docs/metamask-network.json`:

| Field | Value |
|---|---|
| Network Name | JMDT Local |
| RPC URL | `http://localhost:8545` |
| Chain ID | `1337` (or from `eth_chainId`) |
| Currency Symbol | JMDT |
| Block Explorer | `http://localhost:8080` |

---

## Useful `cast` Commands

```bash
# Get block number
cast block-number --rpc-url http://localhost:8545

# Get balance
cast balance <ADDRESS> --rpc-url http://localhost:8545

# Send ETH
cast send <TO> --value 0.1ether --private-key <KEY> --rpc-url http://localhost:8545

# Call a contract function
cast call <CONTRACT> "balanceOf(address)" <ADDRESS> --rpc-url http://localhost:8545
```
