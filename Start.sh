#!/bin/bash

echo "🚀 Starting JMZK Decentralized Network"
echo "====================================="
echo ""
echo "Starting minimal services (only ImmuDB)..."
echo "To enable full monitoring stack, use: docker-compose -f docker-compose-with-loki.yml up -d"
echo ""

docker-compose up -d

echo ""
echo "Starting the application..."
echo "Note: Loki is disabled by default. Use -loki flag to enable Loki logging."
echo ""

# go run . -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -mempool localhost:15051 -did localhost:15052 -cli 15053 -seednode 34.174.233.203:17002
go run . -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -did localhost:15052 -cli 15053 -seednode localhost:17003 -facade 8081 -ws 8086