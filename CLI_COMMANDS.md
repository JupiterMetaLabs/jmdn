# CLI Command Reference for Non-Interactive Mode

## Overview

The JMZK Decentralized Network Node supports two modes:
1. **Interactive Mode**: Connect to the running service and use an interactive shell
2. **Command Mode**: Execute single commands via `./jmdn -cmd <command>`

## Command Execution

When running as a systemd service, use command mode to execute operations:

```bash
./jmdn -cmd <command> [arguments...]
```

## Available Commands

### Commands Available via `-cmd` Flag

All commands listed below can be executed using the command execution mode:

#### Help
```bash
./jmdn -cmd help
```
Displays available commands and usage information.

### Peer Management

#### List Peers
```bash
./jmdn -cmd listpeers
# or
./jmdn -cmd list
```
Lists all managed peers with their status.

#### Add Peer
```bash
./jmdn -cmd addpeer <peer_multiaddr>
```
Adds a peer to the managed peer list.

Example:
```bash
./jmdn -cmd addpeer /ip4/192.168.1.1/tcp/15000/p2p/12D3KooW...
```

#### Remove Peer
```bash
./jmdn -cmd removepeer <peer_id>
```
Removes a peer from the managed peer list.

#### Clean Peers
```bash
./jmdn -cmd cleanpeers
```
Removes offline peers from the managed peer list (peers with 9+ failures).

### Node Information

#### Show Addresses
```bash
./jmdn -cmd addrs
```
Displays all node addresses including Yggdrasil and libp2p.

#### Show Statistics
```bash
./jmdn -cmd stats
```
Displays messaging statistics (sent, received, failed).

#### Show Database State
```bash
./jmdn -cmd dbstate
```
Displays the current state of main and accounts databases.

### Messaging

#### Send Message (libp2p)
```bash
./jmdn -cmd sendmsg <target> <message>
```
Sends a message to a specific peer via libp2p.

Example:
```bash
./jmdn -cmd sendmsg /ip4/192.168.1.1/tcp/15000/p2p/12D3KooW... "Hello!"
```

#### Send Message (Yggdrasil)
```bash
./jmdn -cmd ygg <target> <message>
```
Sends a message via Yggdrasil network.

Example:
```bash
./jmdn -cmd ygg 200::xxxx:xxxx:xxxx "Hello via Ygg!"
```

#### Broadcast Message
```bash
./jmdn -cmd broadcast <message>
```
Broadcasts a message to all connected peers.

Example:
```bash
./jmdn -cmd broadcast "Hello everyone!"
```

#### Send File
```bash
./jmdn -cmd sendfile <peer> <filepath> <remote_filename>
```
Sends a file to a peer.

Example:
```bash
./jmdn -cmd sendfile /ip4/192.168.1.1/tcp/15000/p2p/12D3KooW... ./data.txt data.txt
```

### Blockchain Operations

#### Fast Sync
```bash
./jmdn -cmd fastsync <peer_multiaddr>
```
Performs fast synchronization with a peer.

Example:
```bash
./jmdn -cmd fastsync /ip4/192.168.1.1/tcp/15000/p2p/12D3KooW...
```

### DID Operations

#### Get DID
```bash
./jmdn -cmd getdid <did>
```
Retrieves a DID document from the network.

Example:
```bash
./jmdn -cmd getdid did:example:123
```

### Commands Available ONLY in Interactive Mode

These commands require direct access to node state and are **not exposed via gRPC**:

#### Mempool Statistics
```bash
mempoolStats
```
Shows mempool statistics including fee statistics and queue counts.

#### Seed Node Statistics
```bash
seednodeStats
```
Checks seed node connection, latency, and gets peer statistics.

#### List Aliases
```bash
listaliases
```
Lists all peer aliases from seed node for the current node.

#### Discover Neighbors
```bash
discoverneighbors
```
Discovers and adds neighbors from the seed node.

#### Sync Info
```bash
syncinfo
```
Shows FastSync configuration (batch size, timeouts).

#### gETH Status
```bash
gethstatus
```
Shows gETH server status (chain ID, ports).

#### Propagate DID
```bash
propagateDID <did> <public_key>
```
Propagates a DID to the network (requires database access).

#### Send Message (Interactive)
```bash
msg <peer_multiaddr> <message>
```
Send message via libp2p in interactive mode.

#### Send File (Interactive)
```bash
file <peer_multiaddr> <filepath> [remote_filename]
```
Send file in interactive mode.

### Stop Service

#### Stop Service
```bash
stopservice
```
Stops the running service and exits the program (interactive mode only).

## Quick Command Reference

### Common Tasks

**Check node status:**
```bash
./jmdn -cmd stats        # Messaging statistics
./jmdn -cmd dbstate      # Database state
./jmdn -cmd addrs        # Node addresses
```

**Manage peers:**
```bash
./jmdn -cmd listpeers    # List all peers
./jmdn -cmd cleanpeers   # Remove offline peers
```

**Messaging:**
```bash
./jmdn -cmd broadcast "Hello network!"
./jmdn -cmd sendmsg <peer> "Direct message"
```

**Blockchain:**
```bash
./jmdn -cmd fastsync <peer>  # Sync with peer
./jmdn -cmd getdid <did>      # Get DID document
```

## Notes

### Why Some Commands are Interactive-Only

Certain commands (`mempoolStats`, `seednodeStats`, `listaliases`, `discoverneighbors`, `syncinfo`, `gethstatus`, `propagateDID`) are only available in interactive mode because they:

- Require direct access to node state
- Need real-time data collection
- Involve complex state management
- Should not be exposed via gRPC for security

### Accessing Interactive Mode

The service runs with a gRPC server on port 15053. For interactive mode:

1. Check service status:
   ```bash
   systemctl status jmdn
   ```

2. View service logs:
   ```bash
   journalctl -u jmdn -f
   ```

3. Access the service file log:
   ```bash
   tail -f /root/jmdn.log
   ```

4. Restart service:
   ```bash
   systemctl restart jmdn
   ```

### Architecture

- **Service Mode**: Runs as systemd service with gRPC API on port 15053
- **Command Mode**: Execute via `./jmdn -cmd <command>` (connects to gRPC)
- **Interactive Mode**: Direct access to all commands (requires TTY or direct execution)

The gRPC interface provides the most commonly used commands for automation and remote access, while interactive mode offers full control for administration.

## Troubleshooting

### "Error connecting to gRPC server"

This means the service is not running or not accessible. Check:

```bash
systemctl status jmdn
systemctl start jmdn
```

### "Command not found"

Make sure you're using the `-cmd` flag:

```bash
./jmdn -cmd listpeers  # ✓ Correct
./jmdn listpeers       # ✗ Incorrect - starts service instead
```

### Commands don't exist in non-interactive mode

Some commands are only available when the service starts with the interactive CLI. The gRPC interface exposes a subset of commands for security and reliability reasons.

