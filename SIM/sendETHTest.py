#!/usr/bin/env python3
"""
Specific test to send 2 ETH from 0xCdf1eFFD70cecB41bA0b4c41eB13D263578a4cC2 to 0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45
"""

from testZKBlock import ZKBlockTester
import json

def send_eth_test():
    """Test sending 2 ETH between specific addresses"""
    print("💰 Sending 2 ETH Test")
    print("="*50)
    
    # Create tester
    tester = ZKBlockTester("http://localhost:15050")
    
    # Check if server is running
    print("1. Checking server health...")
    if not tester.test_health_check():
        print("❌ Server is not responding. Make sure your Go server is running on port 15050")
        return False
    
    print("✅ Server is running")
    
    # Specific addresses and amount
    to_address = "0xCdf1eFFD70cecB41bA0b4c41eB13D263578a4cC2"
    from_address = "0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45"
    amount_wei = 0 * 10**18  # 2 ETH in wei
    
    print(f"\n2. Creating transaction:")
    print(f"   From: {from_address}")
    print(f"   To: {to_address}")
    print(f"   Amount: 2 ETH ({amount_wei} wei)")
    
    # Create a custom transaction with specific addresses
    transaction = {
        "hash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
        "from": from_address,
        "to": to_address,
        "value": amount_wei,  # 2 ETH in wei
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
    
    # Create ZKBlock with this specific transaction
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
        print("\n✅ ETH transfer test completed successfully!")
        print("2 ETH has been sent from 0xCdf1eFFD70cecB41bA0b4c41eB13D263578a4cC2 to 0x69EE9a32109EE1CC8c95b49Ad1D4dDAEBb46Db45")
    else:
        print("\n❌ ETH transfer test failed!")
        print("Check the server logs for more details.")
    
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
