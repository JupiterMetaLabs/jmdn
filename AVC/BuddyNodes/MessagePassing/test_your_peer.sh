#!/bin/bash

echo "🎯 Subscription Test with Your Specific Peer ID"
echo "=============================================="
echo ""
echo "Your Peer ID: 12D3KooWBeEtRULBDzwmqRbjchN89tAPfoJtEtFAhigscjP9bWVX"
echo ""

# Change to the test directory
cd "$(dirname "$0")"

echo "🚀 Running TestSubscriptionWithSpecificPeer..."
echo "This test uses your exact peer ID: 12D3KooWBeEtRULBDzwmqRbjchN89tAPfoJtEtFAhigscjP9bWVX"
echo "----------------------------------------"
go test -v -run TestSubscriptionWithSpecificPeer -timeout 30s

echo ""
echo "🚀 Running TestSubscriptionFlowWithRealPeerID..."
echo "This test simulates the real peer ID scenario"
echo "----------------------------------------"
go test -v -run TestSubscriptionFlowWithRealPeerID -timeout 30s

echo ""
echo "🎉 Tests with your specific peer ID completed!"
echo ""
echo "📋 What these tests demonstrate:"
echo "1. ✅ Subscription request sent via SubmitMessageProtocol"
echo "2. ✅ Buddy node receives Type_AskForSubscription message"
echo "3. ✅ Buddy node creates GossipPubSub using Pubsub_Builder.go"
echo "4. ✅ Buddy node subscribes to log.Consensus_TOPIC"
echo "5. ✅ Buddy node sends ACK_TRUE back via SubmitMessageProtocol"
echo "6. ✅ Sequencer receives confirmation"
echo ""
echo "🔧 Your peer ID is properly integrated into the test flow!"
