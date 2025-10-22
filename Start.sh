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

# Function to show jmdn status
show_jmdn_status() {
    echo "📊 jmdn Process Status:"
    echo "======================="
    
    # Check if jmdn process is running
    JMDN_PID=$(pgrep -f "jmdn")
    if [ -z "$JMDN_PID" ]; then
        echo "❌ jmdn is not running"
        return 1
    fi
    
    echo "✅ jmdn is running (PID: $JMDN_PID)"
    echo ""
    echo "📋 Last 30 lines of jmdn output:"
    echo "--------------------------------"
    
    # Try to get logs from various possible locations
    if [ -f "logs/jmdn.log" ]; then
        tail -n 30 logs/jmdn.log
    elif [ -f "jmdn.log" ]; then
        tail -n 30 jmdn.log
    else
        echo "No jmdn log file found. Process is running but no logs available."
        echo "To see real-time logs, run: tail -f logs/jmdn.log"
    fi
}

# Function to show immudb status
show_immudb_status() {
    echo "📊 ImmuDB Process Status:"
    echo "========================="
    
    # Check if immudb process is running
    IMMUDB_PID=$(pgrep -f "immudb")
    if [ -z "$IMMUDB_PID" ]; then
        echo "❌ ImmuDB is not running"
        return 1
    fi
    
    echo "✅ ImmuDB is running (PID: $IMMUDB_PID)"
    echo ""
    echo "📋 Last 30 lines of ImmuDB output:"
    echo "----------------------------------"
    
    # Try to get logs from various possible locations
    if [ -f "logs/ImmuDB.log" ]; then
        tail -n 30 logs/ImmuDB.log
    elif [ -f "ImmuDB.log" ]; then
        tail -n 30 ImmuDB.log
    elif [ -f "logs/immudb.log" ]; then
        tail -n 30 logs/immudb.log
    else
        echo "No ImmuDB log file found. Process is running but no logs available."
        echo "To see real-time logs, run: tail -f logs/ImmuDB.log"
    fi
}

# Function to stop all processes
stop_all_processes() {
    echo "🛑 Stopping JMZK Decentralized Network"
    echo "====================================="
    
    # Stop jmdn process
    JMDN_PID=$(pgrep -f "jmdn")
    if [ ! -z "$JMDN_PID" ]; then
        echo "Stopping jmdn (PID: $JMDN_PID)..."
        kill $JMDN_PID 2>/dev/null
        sleep 2
        # Force kill if still running
        if pgrep -f "jmdn" > /dev/null; then
            echo "Force stopping jmdn..."
            kill -9 $JMDN_PID 2>/dev/null
        fi
        echo "✅ jmdn stopped"
    else
        echo "ℹ️  jmdn is not running"
    fi
    
    # Stop immudb process
    IMMUDB_PID=$(pgrep -f "immudb")
    if [ ! -z "$IMMUDB_PID" ]; then
        echo "Stopping ImmuDB (PID: $IMMUDB_PID)..."
        kill $IMMUDB_PID 2>/dev/null
        sleep 2
        # Force kill if still running
        if pgrep -f "immudb" > /dev/null; then
            echo "Force stopping ImmuDB..."
            kill -9 $IMMUDB_PID 2>/dev/null
        fi
        echo "✅ ImmuDB stopped"
    else
        echo "ℹ️  ImmuDB is not running"
    fi
    
    echo ""
    echo "🏁 All processes stopped"
}

# Function to show help
show_help() {
    echo "Usage: $0 [command] [options]"
    echo ""
    echo "Commands:"
    echo "  start         - Start the JMZK network (default)"
    echo "  exit/stop     - Stop all running processes"
    echo "  status jmdn   - Show jmdn process status and last 30 lines"
    echo "  status immu   - Show ImmuDB process status and last 30 lines"
    echo "  status all    - Show status for both processes"
    echo ""
    echo "Options:"
    echo "  -alias <name> - Set the node alias (default: default-node)"
    echo "  -h, --help   - Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0                                    # Start with default alias"
    echo "  $0 start -alias my-node              # Start with custom alias"
    echo "  $0 status jmdn                       # Check jmdn status"
    echo "  $0 exit                             # Stop all processes"
}

# Function to start the network (main startup logic)
start_network() {
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

    # Check if ImmuDB is running and responding
    echo "Checking ImmuDB status..."
    if ! pgrep -f "immudb" > /dev/null; then
        echo "❌ ImmuDB failed to start"
        exit 1
    fi

    # Try to connect to ImmuDB to verify it's responding
    if command -v immuclient &> /dev/null; then
        # Use immuclient to check connection
        if timeout 5 immuclient status &> /dev/null; then
            echo "✅ ImmuDB is running and responding"
        else
            echo "⚠️  ImmuDB is running but not responding to client connections yet"
            echo "   This is normal during startup, continuing..."
        fi
    else
        echo "✅ ImmuDB process is running (immudb client not available for detailed check)"
    fi

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

    # Display service status before starting jmdn
    echo "📊 Service Status:"
    echo "=================="
    echo "✅ ImmuDB: Running (PID: $IMMUDB_PID)"
    echo "✅ jmdn: Built and ready to start"
    echo ""

    # Start jmdn with the specified parameters
    ./jmdn -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -did localhost:15052 -cli 15053 -seednode 34.174.233.203:17002 -facade 8081 -ws 8086 -alias "$ALIAS" -chainID 7000700
}

# Parse command line arguments
if [ "$1" = "status" ]; then
    if [ "$2" = "jmdn" ]; then
        show_jmdn_status
        exit 0
    elif [ "$2" = "immu" ]; then
        show_immudb_status
        exit 0
    elif [ "$2" = "all" ]; then
        show_immudb_status
        echo ""
        show_jmdn_status
        exit 0
    else
        echo "Usage: $0 status [jmdn|immu|all]"
        echo "  status jmdn  - Show jmdn process status and last 30 lines"
        echo "  status immu  - Show ImmuDB process status and last 30 lines"
        echo "  status all   - Show status for both processes"
        exit 1
    fi
elif [ "$1" = "exit" ] || [ "$1" = "stop" ]; then
    stop_all_processes
    exit 0
elif [ "$1" = "start" ] || [ -z "$1" ]; then
    # start is default behavior, so handle alias parsing
    while [[ $# -gt 0 ]]; do
        case $1 in
            -alias)
                ALIAS="$2"
                shift 2
                ;;
            -h|--help)
                show_help
                exit 0
                ;;
            start)
                shift
                ;;
            *)
                echo "Unknown option $1"
                echo "Use -h or --help for usage information"
                exit 1
                ;;
        esac
    done
    start_network
else
    # Handle help and unknown commands
    if [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
        show_help
        exit 0
    else
        echo "Unknown command: $1"
        echo "Use -h or --help for usage information"
        exit 1
    fi
fi