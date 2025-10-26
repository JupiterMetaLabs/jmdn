#!/bin/bash

echo "🧪 Subscription Flow Test Runner"
echo "================================="
echo ""

# Change to the test directory
cd "$(dirname "$0")"

echo "📋 Available Tests:"
echo "1. TestSubscriptionFlow - Complete flow with two hosts"
echo "2. TestSingleNodeSubscription - Single host self-test"
echo "3. TestSubscriptionWithMockPubSub - Mock PubSub test"
echo "4. TestSubscriptionWithSpecificPeer - Test with your specific peer ID"
echo "5. TestSubscriptionFlowWithRealPeerID - Real peer ID simulation"
echo ""

# Function to run a specific test
run_test() {
    local test_name=$1
    echo "🚀 Running $test_name..."
    echo "----------------------------------------"
    go test -v -run "$test_name" -timeout 30s
    echo ""
}

# Function to run all tests
run_all_tests() {
    echo "🚀 Running All Subscription Tests..."
    echo "====================================="
    go test -v -run "TestSubscription|TestSingleNode" -timeout 60s
}

# Check command line arguments
if [ $# -eq 0 ]; then
    echo "Usage: $0 [test_name|all]"
    echo ""
    echo "Examples:"
    echo "  $0 TestSubscriptionFlow"
    echo "  $0 TestSingleNodeSubscription"
    echo "  $0 TestSubscriptionWithMockPubSub"
    echo "  $0 TestSubscriptionWithSpecificPeer"
    echo "  $0 TestSubscriptionFlowWithRealPeerID"
    echo "  $0 all"
    echo ""
    echo "Running specific peer test by default..."
    run_test "TestSubscriptionWithSpecificPeer"
elif [ "$1" = "all" ]; then
    run_all_tests
else
    run_test "$1"
fi

echo "🎉 Test execution completed!"
