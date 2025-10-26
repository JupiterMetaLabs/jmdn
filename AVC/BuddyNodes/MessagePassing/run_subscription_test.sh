#!/bin/bash

echo "🧪 Running Subscription Flow Tests..."
echo "======================================"

# Change to the test directory
cd "$(dirname "$0")"

# Run the tests with verbose output
echo "Running TestSubscriptionFlow..."
go test -v -run TestSubscriptionFlow

echo ""
echo "Running TestSingleNodeSubscription..."
go test -v -run TestSingleNodeSubscription

echo ""
echo "Running TestSubscriptionWithMockPubSub..."
go test -v -run TestSubscriptionWithMockPubSub

echo ""
echo "Running all subscription tests..."
go test -v -run "TestSubscription|TestSingleNode"

echo ""
echo "🎉 All subscription tests completed!"
