#!/usr/bin/env python3
"""
Test script for processZKBlock endpoint
Tests the /api/process-block endpoint with ZKBlock data
"""

import json
import requests
import time
import random
from datetime import datetime
from eth_account import Account
from eth_utils import to_hex, to_checksum_address
import hashlib

class ZKBlockTester:
    """Test client for ZKBlock processing endpoint"""
    
    def __init__(self, base_url="http://192.168.100.207:15050"):
        self.base_url = base_url.rstrip('/')
        self.session = requests.Session()
        self.session.headers.update({
            'Content-Type': 'application/json',
            'Accept': 'application/json'
        })
    
    def generate_ethereum_keypair(self):
        """Generate an Ethereum compatible keypair"""
        account = Account.create()
        return {
            'private_key': account.key.hex(),
            'address': account.address
        }
    
    def create_sample_transaction(self, from_keypair, to_address, value=1000000000000000000, tx_type=0):
        """Create a sample transaction for the ZKBlock"""
        # Generate transaction hash
        tx_data = f"{from_keypair['address']}{to_address}{value}{int(time.time())}"
        tx_hash = hashlib.sha256(tx_data.encode()).hexdigest()
        
        return {
            "hash": f"0x{tx_hash[:64]}",
            "from": from_keypair['address'],
            "to": to_address,
            "value": value,  # Already an integer
            "type": tx_type,
            "timestamp": int(time.time()),
            "chain_id": int("7000700"),  # Send as integer
            "nonce": random.randint(0, 1000),
            "gas_limit": 21000,
            "gas_price": int("20000000000"),  # Send as integer
            "data": "",
            "v": 27,  # Send as integer
            "r": random.randint(1000000000000000000000000000000000000000000000000000000000000000000000, 9999999999999999999999999999999999999999999999999999999999999999999999),
            "s": random.randint(1000000000000000000000000000000000000000000000000000000000000000000000, 9999999999999999999999999999999999999999999999999999999999999999999999)
        }
    
    def create_sample_zkblock(self, num_transactions=3):
        """Create a sample ZKBlock with transactions"""
        # Generate keypairs for testing
        keypairs = [self.generate_ethereum_keypair() for _ in range(num_transactions + 1)]
        
        # Create transactions
        transactions = []
        for i in range(num_transactions):
            from_addr = keypairs[i]['address']
            to_addr = keypairs[i + 1]['address'] if i < num_transactions - 1 else keypairs[0]['address']
            
            tx = self.create_sample_transaction(
                keypairs[i], 
                to_addr, 
                value=random.randint(1000000000000000000, 10000000000000000000)
            )
            transactions.append(tx)
        
        # Generate block hash
        block_data = f"{int(time.time())}{len(transactions)}{random.randint(1000, 9999)}"
        block_hash = hashlib.sha256(block_data.encode()).hexdigest()
        
        # Generate state root
        state_root = hashlib.sha256(f"state{int(time.time())}".encode()).hexdigest()
        
        # Generate previous hash
        prev_hash = hashlib.sha256(f"prev{int(time.time()) - 1}".encode()).hexdigest()
        
        zkblock = {
            "starkproof": "",
            "commitment": [random.randint(0, 4294967295) for _ in range(8)],
            "proof_hash": f"0x{hashlib.sha256(f'proof{int(time.time())}'.encode()).hexdigest()}",
            "status": "verified",
            "txnsroot": f"0x{hashlib.sha256(f'txns{int(time.time())}'.encode()).hexdigest()}",
            "transactions": transactions,
            "timestamp": int(time.time()),
            "extradata": "0x",
            "stateroot": f"0x{state_root}",
            "logsbloom": "",
            "coinbaseaddr": keypairs[0]['address'],
            "zkvmaddr": keypairs[0]['address'],
            "prevhash": f"0x{prev_hash}",
            "blockhash": f"0x{block_hash}",
            "gaslimit": 30000000,
            "gasused": 21000 * num_transactions,
            "blocknumber": random.randint(1, 1000)
        }
        
        return zkblock
    
    def test_process_zkblock(self, zkblock_data, max_retries=3, timeout=120):
        """Test the processZKBlock endpoint with retry logic and extended timeout"""
        url = f"{self.base_url}/api/process-block"
        
        print(f"Testing ZKBlock processing at {url}")
        print(f"Block Number: {zkblock_data['blocknumber']}")
        print(f"Number of Transactions: {len(zkblock_data['transactions'])}")
        print(f"Block Hash: {zkblock_data['blockhash']}")
        print(f"Status: {zkblock_data['status']}")
        print(f"⏱️  Timeout set to: {timeout}s")
        
        for attempt in range(max_retries):
            try:
                if attempt > 0:
                    wait_time = 2 ** attempt  # Exponential backoff: 2, 4, 8 seconds
                    print(f"🔄 Retry attempt {attempt + 1}/{max_retries} after {wait_time}s...")
                    time.sleep(wait_time)
                
                print(f"📤 Sending request (attempt {attempt + 1}/{max_retries})...")
                response = self.session.post(url, json=zkblock_data, timeout=timeout)
                
                print(f"\nResponse Status: {response.status_code}")
                print(f"Response Headers: {dict(response.headers)}")
                
                if response.status_code == 200:
                    result = response.json()
                    print(f"\n✅ Success! Response:")
                    print(json.dumps(result, indent=2))
                    return True
                else:
                    print(f"\n❌ Error! Status: {response.status_code}")
                    try:
                        error_data = response.json()
                        print(f"Error Response: {json.dumps(error_data, indent=2)}")
                    except:
                        print(f"Error Response (text): {response.text}")
                    
                    # Don't retry on client errors (4xx), only on server errors (5xx)
                    if 400 <= response.status_code < 500:
                        print("🚫 Client error - not retrying")
                        return False
                    
            except requests.exceptions.Timeout as e:
                print(f"\n⏰ Request timed out after {timeout}s: {str(e)}")
                if attempt < max_retries - 1:
                    print(f"🔄 Will retry with extended timeout...")
                    timeout = min(timeout * 1.5, 300)  # Increase timeout up to 5 minutes
                else:
                    print("❌ Max retries reached - giving up")
                    return False
                    
            except requests.exceptions.ConnectionError as e:
                print(f"\n🔌 Connection error: {str(e)}")
                if attempt < max_retries - 1:
                    print("🔄 Will retry...")
                else:
                    print("❌ Max retries reached - giving up")
                    return False
                    
            except requests.exceptions.RequestException as e:
                print(f"\n❌ Request failed: {str(e)}")
                if attempt < max_retries - 1:
                    print("🔄 Will retry...")
                else:
                    print("❌ Max retries reached - giving up")
                    return False
                    
            except Exception as e:
                print(f"\n❌ Unexpected error: {str(e)}")
                return False
        
        return False
    
    def test_invalid_zkblock(self):
        """Test with invalid ZKBlock data to verify error handling"""
        print("\n" + "="*60)
        print("Testing Invalid ZKBlock (should fail)")
        print("="*60)
        
        # Test 1: Empty transactions
        invalid_block1 = self.create_sample_zkblock(0)  # 0 transactions
        invalid_block1['transactions'] = []
        print("\n1. Testing with empty transactions:")
        self.test_process_zkblock(invalid_block1)
        
        # Test 2: Unverified status
        invalid_block2 = self.create_sample_zkblock(2)
        invalid_block2['status'] = "pending"
        print("\n2. Testing with unverified status:")
        self.test_process_zkblock(invalid_block2)
        
        # Test 3: Missing required fields
        invalid_block3 = self.create_sample_zkblock(1)
        del invalid_block3['blockhash']
        print("\n3. Testing with missing blockhash:")
        self.test_process_zkblock(invalid_block3)
    
    def test_health_check(self):
        """Test the health check endpoint"""
        url = f"{self.base_url}/health"
        print(f"\nTesting health check at {url}")
        
        try:
            response = self.session.get(url, timeout=15)
            if response.status_code == 200:
                print("✅ Health check passed")
                return True
            else:
                print(f"❌ Health check failed: {response.status_code}")
                return False
        except Exception as e:
            print(f"❌ Health check error: {str(e)}")
            return False
    
    def run_comprehensive_test(self):
        """Run a comprehensive test suite"""
        print("🚀 Starting ZKBlock Processing Test Suite")
        print("="*60)
        
        # Test health check first
        if not self.test_health_check():
            print("❌ Health check failed. Server may not be running.")
            return False
        
        # Test valid ZKBlock
        print("\n" + "="*60)
        print("Testing Valid ZKBlock")
        print("="*60)
        
        zkblock = self.create_sample_zkblock(3)
        success = self.test_process_zkblock(zkblock)
        
        if success:
            print("\n✅ Valid ZKBlock test passed!")
        else:
            print("\n❌ Valid ZKBlock test failed!")
        
        # Test invalid ZKBlocks
        self.test_invalid_zkblock()
        
        # Test with different transaction counts
        print("\n" + "="*60)
        print("Testing Different Transaction Counts")
        print("="*60)
        
        for tx_count in [1, 5, 10]:
            print(f"\nTesting with {tx_count} transactions:")
            zkblock = self.create_sample_zkblock(tx_count)
            self.test_process_zkblock(zkblock)
            time.sleep(1)  # Small delay between tests
        
        print("\n" + "="*60)
        print("Test Suite Complete")
        print("="*60)
        
        return success

def main():
    """Main function to run the tests"""
    import argparse
    
    parser = argparse.ArgumentParser(description="Test ZKBlock processing endpoint")
    parser.add_argument("--url", default="http://192.168.100.207:15050", 
                       help="Base URL of the API server")
    parser.add_argument("--transactions", type=int, default=3,
                       help="Number of transactions in test block")
    parser.add_argument("--test-invalid", action="store_true",
                       help="Include invalid block tests")
    
    args = parser.parse_args()
    
    # Create tester instance
    tester = ZKBlockTester(args.url)
    
    print(f"Testing ZKBlock processing at {args.url}")
    print(f"Test configuration:")
    print(f"  - Transactions per block: {args.transactions}")
    print(f"  - Include invalid tests: {args.test_invalid}")
    
    if args.test_invalid:
        # Run comprehensive test suite
        tester.run_comprehensive_test()
    else:
        # Run simple valid block test
        zkblock = tester.create_sample_zkblock(args.transactions)
        success = tester.test_process_zkblock(zkblock)
        
        if success:
            print("\n✅ Test completed successfully!")
        else:
            print("\n❌ Test failed!")
            return 1
    
    return 0

if __name__ == "__main__":
    try:
        exit_code = main()
        exit(exit_code)
    except KeyboardInterrupt:
        print("\n\nTest interrupted by user")
        exit(1)
    except Exception as e:
        print(f"\nFatal error: {str(e)}")
        import traceback
        traceback.print_exc()
        exit(1)
