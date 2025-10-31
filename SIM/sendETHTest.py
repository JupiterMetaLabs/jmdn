#!/usr/bin/env python3
"""
Test script to send ETH with properly signed transactions.
This script generates a keypair, signs the transaction using EIP-155,
and includes valid v, r, s signature components that will pass
Security.CheckSignature validation.
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
from eth_account import Account

nodeIP = "localhost"

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

def sign_transaction_with_keypair(private_key_hex, from_address, to_address, value_wei, chain_id, nonce, gas_limit, gas_price, data=""):
    """
    Sign a transaction using a private key and return the signed transaction components.
    
    Args:
        private_key_hex: Private key in hex format (with or without 0x prefix)
        from_address: Sender address (must match the private key)
        to_address: Receiver address
        value_wei: Transaction value in wei
        chain_id: Chain ID for EIP-155 signing
        nonce: Transaction nonce
        gas_limit: Gas limit
        gas_price: Gas price in wei
        data: Transaction data (optional)
    
    Returns:
        Dictionary with v, r, s signature components and the actual from_address
    """
    # Ensure private key is in the correct format
    if private_key_hex.startswith('0x'):
        private_key_hex = private_key_hex[2:]
    
    # Validate that the private key corresponds to the from_address
    account = Account.from_key('0x' + private_key_hex)
    derived_address = account.address
    
    if derived_address.lower() != from_address.lower():
        raise ValueError(f"Private key does not match from_address. "
                        f"Expected {from_address}, got {derived_address}")
    
    # Prepare the transaction dictionary for signing
    transaction_dict = {
        'chainId': int(chain_id),
        'nonce': int(nonce),
        'to': to_address,
        'value': int(value_wei),
        'gas': int(gas_limit),
        'gasPrice': int(gas_price),
        'data': data if data else '0x',
    }
    
    # Sign the transaction
    signed_tx = Account.sign_transaction(transaction_dict, '0x' + private_key_hex)
    
    # Return signature components as integers (Go will parse them as big.Int)
    return {
        'v': signed_tx.v,  # v is an integer
        'r': signed_tx.r,  # r is an integer
        's': signed_tx.s,  # s is an integer
        'from_address': derived_address  # Use the derived address to ensure match
    }

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
    
    # Generate or use a keypair for signing
    # Note: To use a specific address, you need its private key
    # For this test, we'll generate a new keypair and use its address
    step_start = time.time()
    
    # Option 1: Generate a new keypair (recommended for testing)
    account = Account.create()
    from_address = account.address
    private_key_hex = account.key.hex()
    
    # Option 2: Use a specific private key (uncomment and set if you have the private key for the desired address)
    # private_key_hex = "your_private_key_here_without_0x"
    # account = Account.from_key('0x' + private_key_hex)
    # from_address = account.address
    
    to_address = "0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45"
    amount = 1
    amount_wei = int(amount * 10**18)  # 1 ETH in wei
    chain_id = 7000700
    nonce = 0
    gas_limit = 21000
    gas_price = 20000000000  # 20 Gwei
    
    print(f"\n3. Creating and signing transaction:")
    print(f"   From: {from_address}")
    print(f"   To: {to_address}")
    print(f"   Amount: {amount} ETH ({amount_wei} wei)")
    print(f"   Chain ID: {chain_id}")
    print(f"   Nonce: {nonce}")
    
    # Sign the transaction to get valid v, r, s values
    try:
        signature = sign_transaction_with_keypair(
            private_key_hex=private_key_hex,
            from_address=from_address,
            to_address=to_address,
            value_wei=amount_wei,
            chain_id=chain_id,
            nonce=nonce,
            gas_limit=gas_limit,
            gas_price=gas_price,
            data=""
        )
        print(f"✅ Transaction signed successfully")
        print(f"   v: {signature['v']}")
        print(f"   r: {signature['r']}")
        print(f"   s: {signature['s']}")
    except Exception as e:
        print(f"❌ Failed to sign transaction: {e}")
        return False
    
    # Create a custom transaction with signed values
    transaction = {
        "hash": f"0x{secrets.token_hex(32)}",
        "from": signature['from_address'],  # Use the address from the signature
        "to": to_address,
        "value": amount_wei,  # 1 ETH in wei
        "type": 0,
        "timestamp": 1761045000,
        "chain_id": chain_id,
        "nonce": nonce,
        "gas_limit": gas_limit,
        "gas_price": gas_price,
        "data": "",
        "v": signature['v'],  # Actual signature v
        "r": signature['r'],  # Actual signature r
        "s": signature['s']   # Actual signature s
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
        "coinbaseaddr": signature['from_address'],  # Use the signed from_address
        "zkvmaddr": signature['from_address'],      # Use the signed from_address
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
