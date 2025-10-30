#!/usr/bin/env python3
"""
Specific test to send 2 ETH from 0xCdf1eFFD70cecB41bA0b4c41eB13D263578a4cC2 to 0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45
"""

from testZKBlock import ZKBlockTester
import json
import secrets
import requests
import uuid
import time
import hashlib
import random
from datetime import datetime

nodeIP = "34.174.137.160"

def generate_unique_hash():
    """Generate a unique hash for testing"""
    unique_string = f"{time.time()}{uuid.uuid4()}{random.randint(1000, 9999)}"
    return hashlib.sha256(unique_string.encode()).hexdigest()

def format_duration(seconds):
    """Format duration in a human-readable way"""
    if seconds < 1:
        return f"{seconds*1000:.1f}ms"
    elif seconds < 60:
        return f"{seconds:.2f}s"
    else:
        minutes = int(seconds // 60)
        remaining_seconds = seconds % 60
        return f"{minutes}m {remaining_seconds:.1f}s"

def print_timing(step_name, start_time, end_time=None):
    """Print timing information for a step"""
    if end_time is None:
        end_time = time.time()
    duration = end_time - start_time
    print(f"⏱️  {step_name}: {format_duration(duration)}")
    return end_time

def get_latest_block_number():
    """Get the latest block number from the node stats API on port 8090"""
    try:
        # Use the stats API to get the latest block number
        response = requests.get(f"http://{nodeIP}:8090/api/stats/", timeout=10)
        if response.status_code == 200:
            data = response.json()
            # Get the latest block number from stats
            latest_block_number = data.get('LatestBlockNumber', 0)
            return latest_block_number
        print(f"⚠️  Warning: Could not get latest block number from stats API (status: {response.status_code})")
        return 0
    except requests.exceptions.RequestException as e:
        print(f"⚠️  Warning: Could not connect to node on port 8090: {e}")
        return 0

def send_eth_test():
    """Test sending 2 ETH between specific addresses"""
    test_start_time = time.time()
    print("💰 Sending 2 ETH Test")
    print("="*50)
    print(f"🕐 Test started at: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    
    # Create tester with extended timeout
    tester = ZKBlockTester(f"http://{nodeIP}:15050")
    
    # Check if server is running
    step_start = time.time()
    print("1. Checking server health...")
    if not tester.test_health_check():
        print("❌ Server is not responding. Make sure your Go server is running on port 15050")
        return False
    
    print("✅ Server is running")
    print_timing("Health check", step_start)
    
    # Get the latest block number from the node stats API
    step_start = time.time()
    print("2. Getting latest block number from stats API...")
    latest_block_number = get_latest_block_number()
    if latest_block_number > 0:
        print(f"✅ Latest block number from stats: {latest_block_number}")
    else:
        print("⚠️  Using default block number: 8")
        latest_block_number = 8
    print_timing("Block number retrieval", step_start)
    
    # Specific addresses and amount
    step_start = time.time()
    to_address = "0xCdf1eFFD70cecB41bA0b4c41eB13D263578a4cC2"
    from_address = "0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45"
    amount = 1
    amount_wei = int(amount * 10**18)  # 1 ETH in wei
    
    print(f"\n3. Creating transaction:")
    print(f"   From: {from_address}")
    print(f"   To: {to_address}")
    print(f"   Amount: {amount} ETH ({amount_wei} wei)")
    
    # Create a custom transaction with specific addresses
    transaction = {
        "hash": f"0x{secrets.token_hex(32)}",
        "from": from_address,
        "to": to_address,
        "value": amount_wei,  # 1 ETH in wei
        "type": 0,
        "timestamp": 1761045000,
        "chain_id": 7000700,
        "nonce": 0,
        "gas_limit": 21000,
        "gas_price": 20000000000,  # 20 Gwei
        "data": "",
        "v": 27,
        "r": 1234567890123456789012345678901234567890123456789012345678901234567890,
        "s": 9876543210987654321098765432109876543210987654321098765432109876543210
    }
    
    # Generate unique hashes for this test
    unique_proof_hash = "0x" + generate_unique_hash()
    unique_txnsroot = "0x" + generate_unique_hash()
    unique_stateroot = "0x" + generate_unique_hash()
    unique_prevhash = "0x" + generate_unique_hash()
    unique_blockhash = "0x" + generate_unique_hash()
    
    print(f"🔑 Generated unique block hash: {unique_blockhash}")
    
    # Create ZKBlock with this specific transaction
    zkblock = {
        "starkproof": "",
        "commitment": [1234567890, 2345678901, 3456789012, 1234567890, 2345678901, 3456789012, 1234567890, 2345678901],
        "proof_hash": unique_proof_hash,
        "status": "verified",
        "txnsroot": unique_txnsroot,
        "transactions": [transaction],
        "timestamp": 1761045000,
        "extradata": "0x",
        "stateroot": unique_stateroot,
        "logsbloom": "",
        "coinbaseaddr": from_address,
        "zkvmaddr": from_address,
        "prevhash": unique_prevhash,
        "blockhash": unique_blockhash,
        "gaslimit": 30000000,
        "gasused": 21000,
        "blocknumber": latest_block_number + 1
    }
    
    print(f"\n4. ZKBlock Details:")
    print(f"   Block Number: {zkblock['blocknumber']}")
    print(f"   Transactions: {len(zkblock['transactions'])}")
    print(f"   Status: {zkblock['status']}")
    print(f"   Block Hash: {zkblock['blockhash']}")
    print_timing("Transaction and ZKBlock creation", step_start)
    
    # Test the block processing
    step_start = time.time()
    print(f"\n5. Testing ZKBlock processing...")
    print("⏳ Processing may take up to 2 minutes...")
    success = tester.test_process_zkblock(zkblock)
    processing_time = print_timing("ZKBlock processing", step_start)
    
    # Print final results with timing summary
    total_time = time.time() - test_start_time
    print(f"\n{'='*50}")
    print("📊 TIMING SUMMARY")
    print(f"{'='*50}")
    print(f"⏱️  Total test duration: {format_duration(total_time)}")
    print(f"🕐 Test completed at: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    
    if success:
        print("\n✅ JMDT transfer test completed successfully!")
        print(f"{amount} JMDT has been sent from {from_address} to {to_address} on node {nodeIP}")
        print(f"🚀 Processing completed in {format_duration(processing_time)}")
    else:
        print("\n❌ JMDT transfer test failed!")
        print("Check the server logs for more details.")
        print(f"⏰ Failed after {format_duration(total_time)}")
    
    return success

if __name__ == "__main__":
    try:
        success = send_eth_test()
        exit(0 if success else 1)
    except KeyboardInterrupt:
        print("\nTest interrupted by user")
        exit(1)
    except Exception as e:
        print(f"Error: {str(e)}")
        exit(1)
