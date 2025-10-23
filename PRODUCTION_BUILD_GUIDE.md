# JMDT Decentralized Network - Production Node Building Guide

## Table of Contents
1. [Repository Access](#repository-access)
2. [Overview](#overview)
3. [System Requirements](#system-requirements)
4. [Dependencies](#dependencies)
5. [Build Process](#build-process)
6. [Production Deployment](#production-deployment)
7. [Configuration](#configuration)
8. [Monitoring & Observability](#monitoring--observability)
9. [Security Considerations](#security-considerations)
10. [Performance Tuning](#performance-tuning)
11. [Operational Procedures](#operational-procedures)
12. [Troubleshooting](#troubleshooting)
13. [Maintenance](#maintenance)

## Repository Access

**⚠️ Important**: This repository is private and requires access approval. To request access to the JMDT Decentralized Network repository, please contact:

**Email**: saishibu@jmdt.io  
**Telegram**: @DrSaiShibu

Include the following information in your access request:
- Your name and organization
- Intended use case for the JMDT Decentralized Network
- Your GitHub username
- Brief description of your project or research

Access will be granted based on legitimate use cases and compliance with the project's terms of use.

## Overview

The JMDT Decentralized Network is a sophisticated peer-to-peer blockchain system built in Go that combines multiple advanced distributed systems concepts. This guide provides comprehensive instructions for building and deploying production-ready nodes.

### Key Features
- **Libp2p Network Layer**: Foundation for peer discovery and communication
- **ImmuDB Integration**: Tamper-proof key-value database with Merkle trees
- **FastSync Protocol**: Efficient blockchain state synchronization
- **CRDT Engine**: Conflict-free replicated data types
- **Gossip Protocol**: Reliable information dissemination
- **Bloom Filter Optimization**: Memory-efficient duplicate detection
- **Ethereum Compatibility**: gETH facade for Ethereum tool compatibility
- **Decentralized Identity**: DID management system
- **Comprehensive Monitoring**: Prometheus metrics and Loki logging

## System Requirements

### Minimum Requirements
- **CPU**: 2 cores, 2.0 GHz
- **RAM**: 4 GB
- **Storage**: 50 GB SSD
- **Network**: 100 Mbps connection
- **OS**: Ubuntu 20.04+ LTS, Debian 11+, or macOS 12+

### Recommended Production Requirements
- **CPU**: 4+ cores, 3.0+ GHz
- **RAM**: 8+ GB
- **Storage**: 200+ GB NVMe SSD
- **Network**: 1 Gbps connection with low latency
- **OS**: Ubuntu 22.04 LTS or Debian 12+

### Supported Architectures
- **x86_64 (amd64)**: Primary supported architecture
- **ARM64 (aarch64)**: Full support for ARM-based systems
- **ARMv7**: Limited support for older ARM systems

## Dependencies

### Core Dependencies
1. **Go 1.23+**: Programming language runtime
2. **ImmuDB**: Tamper-proof key-value database
3. **Yggdrasil**: Decentralized mesh networking (optional)
4. **Docker & Docker Compose**: Containerization (optional)

### System Dependencies
```bash
# Ubuntu/Debian
sudo apt update
sudo apt install -y curl wget git build-essential

# macOS
brew install curl wget git
```

## Build Process

### 1. Automated Setup (Recommended)

The easiest way to set up the environment is using the provided setup script:

```bash
# Clone the repository (private access required)
git clone https://github.com/your-org/JMZK-Decentalized-Network.git
cd JMZK-Decentalized-Network

# Make setup script executable and run it
chmod +x Setup.sh
./Setup.sh
```

This script automatically installs all prerequisites in the correct order:
1. **Go Programming Language** - Latest version with proper PATH configuration
2. **ImmuDB** - Tamper-proof key-value database with OS detection
3. **Yggdrasil Network** - Official Debian package installation
4. **Docker** (optional) - Uncomment in Setup.sh if needed

### 2. Manual Installation

If you prefer manual installation or need to install dependencies individually:

#### Install Go
```bash
chmod +x ./Scripts/Go_Prerequisite.sh
./Scripts/Go_Prerequisite.sh
```

#### Install ImmuDB
```bash
chmod +x ./Scripts/ImmuDB_Prerequisite.sh
./Scripts/ImmuDB_Prerequisite.sh
```

#### Install Yggdrasil
```bash
chmod +x ./Scripts/YGG_Prerequisite.sh
./Scripts/YGG_Prerequisite.sh
```

#### Install Docker (Optional)
```bash
chmod +x ./Scripts/Docker_Prerequisite.sh
./Scripts/Docker_Prerequisite.sh
```

### 3. Build the Application

After installing prerequisites:

```bash
# Install Go dependencies
go mod download

# Build the application with production optimizations
go build -ldflags='-linkmode=external -w -s' -o jmdn .
```

**Build Flags Explanation:**
- `-linkmode=external`: Use external linking for better performance
- `-w`: Strip DWARF debug information to reduce binary size
- `-s`: Strip symbol table to reduce binary size

### 4. Verify Build

```bash
# Check if the binary was created successfully
ls -la jmdn

# Test the binary
./jmdn --help
```

## Production Deployment

### 1. Docker Deployment (Recommended)

#### Using Docker Compose

**Minimal Setup:**
```bash
# Start only ImmuDB
docker-compose -f docker-compose-minimal.yml up -d
```

**Full Setup with Monitoring:**
```bash
# Uncomment monitoring services in docker-compose.yml first
docker-compose up -d
```

#### Custom Docker Build

Create a `Dockerfile` for the node:

```dockerfile
FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY . .

# Install build dependencies
RUN apk add --no-cache git gcc musl-dev

# Build the application
RUN go mod download
RUN go build -ldflags='-linkmode=external -w -s' -o jmdn .

FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates

WORKDIR /root/

# Copy the binary
COPY --from=builder /app/jmdn .

# Create necessary directories
RUN mkdir -p logs data

# Expose ports
EXPOSE 8082 8081 8086 15050 15051 15052 15053 15054

# Set default command
CMD ["./jmdn", "-heartbeat", "10", "-metrics", "8082", "-api", "8090", "-blockgen", "15050", "-did", "localhost:15052", "-cli", "15053", "-seednode", "seed.jmdt.io:443", "-facade", "8081", "-ws", "8086", "-chainID", "7000700"]
```

### 2. Systemd Service Deployment

Create a systemd service file for production deployment:

```bash
sudo nano /etc/systemd/system/jmdn.service
```

```ini
[Unit]
Description=JMDT Decentralized Network Node
After=network.target
Wants=network.target

[Service]
Type=simple
User=jmdn
Group=jmdn
WorkingDirectory=/opt/jmdn
ExecStart=/opt/jmdn/jmdn -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -did localhost:15052 -cli 15053 -seednode testseed.jmdt.io:443 -facade 8081 -ws 8086 -chainID 7000700
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=jmdn

# Security settings
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/jmdn/data /opt/jmdn/logs

# Resource limits
LimitNOFILE=65536
LimitNPROC=32768

[Install]
WantedBy=multi-user.target
```

Enable and start the service:

```bash
# Create user and directories
sudo useradd -r -s /bin/false jmdn
sudo mkdir -p /opt/jmdn/{data,logs}
sudo chown -R jmdn:jmdn /opt/jmdn

# Copy binary
sudo cp jmdn /opt/jmdn/
sudo chown jmdn:jmdn /opt/jmdn/jmdn
sudo chmod +x /opt/jmdn/jmdn

# Enable and start service
sudo systemctl daemon-reload
sudo systemctl enable jmdn
sudo systemctl start jmdn

# Check status
sudo systemctl status jmdn
```

### 3. Process Management with Start.sh

The provided `Start.sh` script offers comprehensive process management:

```bash
# Start in foreground
./Start.sh start -alias production-node

# Start in daemon mode
./Start.sh daemon -alias production-node

# Check status
./Start.sh status all

# Stop all processes
./Start.sh exit
```

## Configuration

### Command Line Flags

| Flag | Description | Default | Production Recommendation |
|------|-------------|---------|---------------------------|
| `-seednode` | Seed node gRPC URL | `""` | Production seed node URL |
| `-alias` | Node alias | `""` | Unique identifier |
| `-heartbeat` | Heartbeat interval (seconds) | `120` | `10-30` for production |
| `-metrics` | Prometheus metrics port | `"8080"` | `8082` |
| `-api` | ImmuDB API port | `0` | `8090` for production |
| `-blockgen` | Block generator port | `0` | `15050` for production |
| `-did` | DID gRPC server | `localhost:15052` | `localhost:15052` |
| `-cli` | CLI gRPC server | `15053` | `15053` |
| `-geth` | gETH gRPC server | `15054` | `15054` |
| `-facade` | gETH Facade port | `15001` | `8081` for production |
| `-ws` | WebSocket server port | `15002` | `8086` for production |
| `-chainID` | Blockchain chain ID | `7000700` | Production chain ID |
| `-ygg` | Enable Yggdrasil | `true` | `true` for production |
| `-loki` | Enable Loki logging | `false` | `true` for production |


### Configuration Files

#### ImmuDB Configuration
```yaml
# /etc/immudb/immudb.yaml
log-level: info
mtls: false
max-recv-msg-size: 16777216
port: 3322
address: 0.0.0.0
```

#### Yggdrasil Configuration
```json
{
  "Peers": [],
  "InterfacePeers": {},
  "AllowedPublicKeys": [],
  "Listen": ["tcp://0.0.0.0:15000"],
  "AdminListen": "tcp://localhost:9001"
}
```

## Monitoring & Observability (Optional)

### 1. Prometheus Metrics

The node exposes Prometheus-compatible metrics on port 8082 (optional):

```bash
# Access metrics
curl http://localhost:8082/metrics

# Key metrics to monitor:
# - jmdn_connections_total
# - jmdn_messages_total
# - jmdn_blocks_processed_total
# - jmdn_database_operations_total
# - jmdn_peer_count
```

### 2. Grafana Dashboards

Import the provided Grafana dashboards from `grafana/provisioning/dashboards/`:

```bash
# Start Grafana with dashboards
docker-compose up -d grafana

# Access Grafana
open http://localhost:3000
# Default credentials: admin/password
```

### 3. Loki Logging

Enable structured logging with Loki:

```bash
# Start Loki and Promtail
docker-compose up -d loki promtail

# Configure logging in your node
./jmdn -loki=true -log-level=info
```

### 4. Health Checks

Implement health check endpoints:

```bash
# Node health
curl http://localhost:8082/health

# Database health
curl http://localhost:8090/api/health

# Peer connectivity
curl http://localhost:8082/peers
```

### 5. Alerting Rules

Create Prometheus alerting rules:

```yaml
# prometheus-alerts.yml
groups:
- name: jmdn
  rules:
  - alert: NodeDown
    expr: up{job="jmdn"} == 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "JMDT node is down"
      
  - alert: HighPeerCount
    expr: jmdn_peer_count > 7
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "High peer count detected"
```

## Security Considerations

### 1. Network Security

- **Firewall Configuration**: Only expose necessary ports

### 2. Access Control

- **User Permissions**: Run services with minimal privileges
- **API Authentication**: Implement authentication for API endpoints
- **Rate Limiting**: Implement rate limiting for public endpoints

### 3. Data Security

- **Encryption**: Enable encryption at rest for ImmuDB
- **Backup Encryption**: Encrypt database backups
- **Key Management**: Secure private key storage

### 4. System Hardening

```bash
# Disable unnecessary services
sudo systemctl disable bluetooth
sudo systemctl disable cups

# Configure fail2ban
sudo apt install fail2ban
sudo systemctl enable fail2ban

# Set up automatic security updates
sudo apt install unattended-upgrades
sudo dpkg-reconfigure unattended-upgrades
```

## Operational Procedures

### 1. Node Startup Sequence

```bash
# 1. Start ImmuDB
sudo systemctl start immudb
sleep 10

# 2. Verify ImmuDB is ready
immuclient status

# 3. Start JMDT node
sudo systemctl start jmdn

# 4. Verify node is running
sudo systemctl status jmdn
```

### 2. Graceful Shutdown

```bash
# Stop node gracefully
sudo systemctl stop jmdn

# Wait for cleanup
sleep 5

# Stop ImmuDB
sudo systemctl stop immudb
```

## Troubleshooting

### 1. Common Issues

#### Node Won't Start
```bash
# Check logs
sudo journalctl -u jmdn -f

# Check port conflicts
sudo netstat -tlnp | grep :8081

# Check permissions
ls -la /opt/jmdn/
```

#### Database Connection Issues
```bash
# Check ImmuDB status
sudo systemctl status immudb
immuclient status

# Check database connectivity
telnet localhost 3322

# Restart ImmuDB
sudo systemctl restart immudb
```

#### Network Connectivity Issues
```bash
# Check peer connections
curl http://localhost:8080/peers

# Test seed node connectivity
telnet testseed.jmdt.io 443

# Check firewall
sudo ufw status
```

### 2. Log Analysis

```bash
# View real-time logs
sudo journalctl -u jmdn -f

# Search for errors
sudo journalctl -u jmdn | grep -i error

# Export logs
sudo journalctl -u jmdn --since "1 hour ago" > jmdn_logs.txt
```

### 3. Performance Issues

```bash
# Monitor resource usage
htop
iotop
nethogs

# Check database performance
immuclient scan defaultdb

# Monitor network connections
ss -tuln | grep :8082
```

### 4. Recovery Procedures

#### Database Recovery
```bash
# Stop services
sudo systemctl stop jmdn immudb

# Restore from backup
sudo -u immudb immudb restore --input /opt/backups/immudb_backup_20240101_120000

# Start services
sudo systemctl start immudb
sleep 10
sudo systemctl start jmdn
```

#### Node Recovery
```bash
# Reset node state (if needed)
sudo systemctl stop jmdn
rm -rf /opt/jmdn/data/peerstore
sudo systemctl start jmdn
```

## Maintenance

### 1. Regular Maintenance Tasks

#### Daily
- Monitor node status and metrics
- Check log files for errors
- Verify peer connectivity

#### Weekly
- Review performance metrics
- Check disk space usage
- Update security patches

#### Monthly
- Perform database backups
- Review and rotate logs
- Update node software

### 2. Monitoring Checklist

- [ ] Node is running and responsive
- [ ] Database is accessible
- [ ] Peer connections are active
- [ ] Metrics are being collected
- [ ] Logs are being written
- [ ] Disk space is adequate
- [ ] Memory usage is normal
- [ ] Network connectivity is stable

### 3. Performance Monitoring

```bash
# Create monitoring script
cat > /opt/jmdn/monitor.sh << 'EOF'
#!/bin/bash

# Check node status
if ! systemctl is-active --quiet jmdn; then
    echo "ERROR: jmdn service is not running"
    exit 1
fi

# Check database connectivity
if ! immuclient status >/dev/null 2>&1; then
    echo "ERROR: ImmuDB is not accessible"
    exit 1
fi

# Check metrics endpoint
if ! curl -s http://localhost:8082/metrics >/dev/null; then
    echo "ERROR: Metrics endpoint is not responding"
    exit 1
fi

echo "All checks passed"
EOF

chmod +x /opt/jmdn/monitor.sh

# Add to crontab
echo "*/5 * * * * /opt/jmdn/monitor.sh" | crontab -
```

### 4. Log Rotation

```bash
# Configure logrotate
sudo nano /etc/logrotate.d/jmdn
```

```
/opt/jmdn/logs/*.log {
    daily
    missingok
    rotate 30
    compress
    delaycompress
    notifempty
    create 644 jmdn jmdn
    postrotate
        systemctl reload jmdn
    endscript
}
```

This comprehensive guide provides everything needed to build, deploy, and maintain production-ready JMZK Decentralized Network nodes. Follow the procedures carefully and adapt them to your specific environment and requirements.
