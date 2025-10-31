#!/usr/bin/env python3
"""
Custom transfer test script for ZKBlock processing
Allows you to specify from/to addresses and amount
"""

from testZKBlock import ZKBlockTester
import json
import sys

def create_custom_transfer(from_address, to_address, amount_eth):
    """Create a custom ETH transfer ZKBlock"""
    print(f"💰 Creating {amount_eth} ETH transfer")
    print("="*50)
    
    # Create tester
    tester = ZKBlockTester("http://192.168.100.24:15050")
    
    # Check if server is running
    print("1. Checking server health...")
    if not tester.test_health_check():
        print("❌ Server is not responding. Make sure your Go server is running on port 15050")
        return False
    
    print("✅ Server is running")
    
    # Convert ETH to wei
    amount_wei = int(amount_eth * 10**18)
    
    print(f"\n2. Transfer Details:")
    print(f"   From: {from_address}")
    print(f"   To: {to_address}")
    print(f"   Amount: {amount_eth} ETH ({amount_wei} wei)")
    
    # Create a custom transaction
    transaction = {
        "hash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
        "from": from_address,
        "to": to_address,
        "value": amount_wei,
        "type": 0,
        "timestamp": 1761045000,
        "chain_id": 8000800,
        "nonce": 0,
        "gas_limit": 21000,
        "gas_price": 20000000000,  # 20 Gwei
        "data": "",
        "v": 27,
        "r": 1234567890123456789012345678901234567890123456789012345678901234567890,
        "s": 9876543210987654321098765432109876543210987654321098765432109876543210
    }
    
    # Create ZKBlock with this transaction
    zkblock = {
        "starkproof": "",
        "commitment": [1234567890, 2345678901, 3456789012, 1234567890, 2345678901, 3456789012, 1234567890, 2345678901],
        "proof_hash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
        "status": "verified",
        "txnsroot": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
        "transactions": [transaction],
        "timestamp": 1761045000,
        "extradata": "0x",
        "stateroot": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
        "logsbloom": "",
        "coinbaseaddr": from_address,
        "zkvmaddr": from_address,
        "prevhash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
        "blockhash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
        "gaslimit": 30000000,
        "gasused": 21000,
        "blocknumber": 1
    }
    
    print(f"\n3. ZKBlock Details:")
    print(f"   Block Number: {zkblock['blocknumber']}")
    print(f"   Transactions: {len(zkblock['transactions'])}")
    print(f"   Status: {zkblock['status']}")
    print(f"   Block Hash: {zkblock['blockhash']}")
    
    # Test the block processing
    print(f"\n4. Testing ZKBlock processing...")
    success = tester.test_process_zkblock(zkblock)
    
    if success:
        print(f"\n✅ Transfer test completed successfully!")
        print(f"{amount_eth} ETH has been sent from {from_address} to {to_address}")
    else:
        print(f"\n❌ Transfer test failed!")
        print("Check the server logs for more details.")
    
    return success

def main():
    """Main function with command line arguments"""
    if len(sys.argv) != 4:
        print("Usage: python customTransferTest.py <from_address> <to_address> <amount_eth>")
        print("Example: python customTransferTest.py 0xCdf1eFFD70cecB41bA0b4c41eB13D263578a4cC2 0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45 2")
        sys.exit(1)
    
    from_address = sys.argv[1]
    to_address = sys.argv[2]
    amount_eth = float(sys.argv[3])
    
    try:
        success = create_custom_transfer(from_address, to_address, amount_eth)
        exit(0 if success else 1)
    except KeyboardInterrupt:
        print("\nTest interrupted by user")
        exit(1)
    except Exception as e:
        print(f"Error: {str(e)}")
        exit(1)

if __name__ == "__main__":
    main()
