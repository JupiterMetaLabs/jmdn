#!/bin/bash

# Default alias if not provided
ALIAS="default-node"

# Cleanup function to kill immudb when script exits
cleanup() {
    if [ ! -z "$IMMUDB_PID" ]; then
        echo ""
        echo "Stopping ImmuDB (PID: $IMMUDB_PID)..."
        kill $IMMUDB_PID 2>/dev/null
    fi
}

# Set trap to cleanup on script exit
trap cleanup EXIT

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -alias)
            ALIAS="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [-alias <node-alias>]"
            echo "  -alias: Set the node alias (default: default-node)"
            exit 0
            ;;
        *)
            echo "Unknown option $1"
            echo "Use -h or --help for usage information"
            exit 1
            ;;
    esac
done

echo "🚀 Starting JMZK Decentralized Network"
echo "====================================="
echo "Node Alias: $ALIAS"
echo ""

echo "Checking ImmuDB installation..."
# Check if immudb is installed
if ! command -v immudb &> /dev/null; then
    echo "❌ ImmuDB is not installed or not in PATH"
    echo "Please install ImmuDB first:"
    echo "  curl -L https://github.com/codenotary/immudb/releases/latest/download/immudb-v1.5.2-linux-amd64 -o immudb"
    echo "  chmod +x immudb"
    echo "  sudo mv immudb /usr/local/bin/"
    exit 1
fi

echo "✅ ImmuDB is installed"

echo ""
echo "Starting ImmuDB..."
# Start ImmuDB in the background
immudb --dir ./data --web-server &

# Store the PID for potential cleanup
IMMUDB_PID=$!

echo "ImmuDB started with PID: $IMMUDB_PID"
echo "Waiting for ImmuDB to be ready..."
sleep 3

echo ""
echo "Building jmdn executable..."
# Build the jmdn executable with optimized flags
go build -ldflags='-linkmode=external -w -s' -o jmdn .

if [ $? -ne 0 ]; then
    echo "❌ Failed to build jmdn executable"
    exit 1
fi

echo "✅ jmdn executable built successfully"
echo ""

echo "Starting jmdn with alias: $ALIAS"
echo "Command: ./jmdn -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -did localhost:15052 -cli 15053 -seednode 34.174.233.203:17002 -facade 8081 -ws 8086 -alias \"$ALIAS\" -chainID 7000700"
echo ""

# Start jmdn with the specified parameters
./jmdn -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -did localhost:15052 -cli 15053 -seednode 34.174.233.203:17002 -facade 8081 -ws 8086 -alias "$ALIAS" -chainID 7000700