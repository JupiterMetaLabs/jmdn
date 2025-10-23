#!/usr/bin/env python3
"""
Simple example script to test ZKBlock processing
This is a minimal example showing how to use the ZKBlockTester
"""

from testZKBlock import ZKBlockTester
import json

def simple_test():
    """Run a simple test of the ZKBlock processing"""
    print("🧪 Simple ZKBlock Test")
    print("="*40)
    
    # Create tester (adjust URL if your server runs on different port)
    tester = ZKBlockTester("http://192.168.100.24:15050")
    
    # Check if server is running
    print("1. Checking server health...")
    if not tester.test_health_check():
        print("❌ Server is not responding. Make sure your Go server is running on port 15050")
        return False
    
    print("✅ Server is running")
    
    # Create a simple ZKBlock with 2 transactions
    print("\n2. Creating test ZKBlock...")
    zkblock = tester.create_sample_zkblock(2)
    
    print(f"   Block Number: {zkblock['blocknumber']}")
    print(f"   Transactions: {len(zkblock['transactions'])}")
    print(f"   Status: {zkblock['status']}")
    
    # Test the block processing
    print("\n3. Testing ZKBlock processing...")
    success = tester.test_process_zkblock(zkblock)
    
    if success:
        print("\n✅ ZKBlock test completed successfully!")
        print("Your processZKBlock endpoint is working correctly.")
    else:
        print("\n❌ ZKBlock test failed!")
        print("Check the server logs for more details.")
    
    return success

if __name__ == "__main__":
    try:
        success = simple_test()
        exit(0 if success else 1)
    except KeyboardInterrupt:
        print("\nTest interrupted by user")
        exit(1)
    except Exception as e:
        print(f"Error: {str(e)}")
        exit(1)
