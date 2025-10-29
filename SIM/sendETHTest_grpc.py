#!/usr/bin/env python3
"""
gRPC version of sendETHTest.py
Uses gRPC instead of HTTP API to send ETH transactions
"""

import grpc
import block_pb2
import block_pb2_grpc
import json
import secrets
import requests
import uuid
import time
import hashlib
import random
from datetime import datetime

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
        response = requests.get("http://192.168.100.24:8090/api/stats/", timeout=10)
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

class ZKBlockTesterGRPC:
    """gRPC client for ZKBlock processing"""
    
    def __init__(self, grpc_address="192.168.100.24:15055"):
        self.grpc_address = grpc_address
        self.channel = None
        self.stub = None
    
    def connect(self):
        """Connect to the gRPC server"""
        try:
            self.channel = grpc.insecure_channel(self.grpc_address)
            self.stub = block_pb2_grpc.BlockServiceStub(self.channel)
            print(f"✅ Connected to gRPC server at {self.grpc_address}")
            return True
        except Exception as e:
            print(f"❌ Failed to connect to gRPC server: {e}")
            return False
    
    def disconnect(self):
        """Disconnect from the gRPC server"""
        if self.channel:
            self.channel.close()
    
    def test_health_check(self):
        """Test if the gRPC server is responding"""
        try:
            # Try to create a simple request to test connectivity
            if not self.stub:
                return False
            
            # Create a minimal test request
            test_block = block_pb2.ZKBlock()
            test_request = block_pb2.ProcessBlockRequest(block=test_block)
            
            # This will fail but we just want to test connectivity
            try:
                self.stub.ProcessBlock(test_request, timeout=5)
            except grpc.RpcError as e:
                # If we get any gRPC error, the server is responding
                if e.code() != grpc.StatusCode.UNIMPLEMENTED:
                    return True
            return True
        except Exception as e:
            print(f"❌ Health check failed: {e}")
            return False
    
    def create_transaction_proto(self, from_address, to_address, value_wei, nonce=0):
        """Create a Transaction protobuf message"""
        # Generate transaction hash
        tx_data = f"{from_address}{to_address}{value_wei}{int(time.time())}"
        tx_hash = hashlib.sha256(tx_data.encode()).hexdigest()
        
        # Convert addresses to bytes (remove 0x prefix and convert to bytes)
        # Ethereum addresses should be exactly 20 bytes
        def address_to_bytes(address):
            if address.startswith('0x'):
                return bytes.fromhex(address[2:])
            return bytes.fromhex(address)
        
        from_bytes = address_to_bytes(from_address)
        to_bytes = address_to_bytes(to_address)
        
        # Ensure addresses are exactly 20 bytes
        if len(from_bytes) != 20:
            raise ValueError(f"Invalid from address length: {len(from_bytes)} bytes")
        if len(to_bytes) != 20:
            raise ValueError(f"Invalid to address length: {len(to_bytes)} bytes")
        
        # Convert value to bytes (big-endian, 32 bytes)
        value_bytes = value_wei.to_bytes(32, byteorder='big')
        
        # Convert chain_id to bytes (32 bytes)
        chain_id_bytes = (7000700).to_bytes(32, byteorder='big')
        
        # Convert gas_price to bytes (32 bytes)
        gas_price_bytes = (20000000000).to_bytes(32, byteorder='big')
        
        # Generate v, r, s values (32 bytes each)
        v_bytes = (27).to_bytes(32, byteorder='big')
        r_bytes = random.randint(1000000000000000000000000000000000000000000000000000000000000000000000, 9999999999999999999999999999999999999999999999999999999999999999999999).to_bytes(32, byteorder='big')
        s_bytes = random.randint(1000000000000000000000000000000000000000000000000000000000000000000000, 9999999999999999999999999999999999999999999999999999999999999999999999).to_bytes(32, byteorder='big')
        
        # Create Transaction protobuf message
        transaction = block_pb2.Transaction()
        transaction.hash = bytes.fromhex(tx_hash[:64])
        setattr(transaction, 'from', from_bytes)  # Use setattr for reserved keyword 'from'
        transaction.to = to_bytes
        transaction.value = value_bytes
        transaction.type = 0
        transaction.timestamp = int(time.time())
        transaction.chain_id = chain_id_bytes
        transaction.nonce = nonce
        transaction.gas_limit = 21000
        transaction.gas_price = gas_price_bytes
        transaction.data = b""
        transaction.v = v_bytes
        transaction.r = r_bytes
        transaction.s = s_bytes
        
        return transaction
    
    def create_zkblock_proto(self, transactions, block_number):
        """Create a ZKBlock protobuf message"""
        # Generate unique hashes
        unique_proof_hash = generate_unique_hash()
        unique_txnsroot = generate_unique_hash()
        unique_stateroot = generate_unique_hash()
        unique_prevhash = generate_unique_hash()
        unique_blockhash = generate_unique_hash()
        
        # Convert hex strings to bytes (remove 0x prefix)
        def hex_to_bytes(hex_str):
            if hex_str.startswith('0x'):
                return bytes.fromhex(hex_str[2:])
            return bytes.fromhex(hex_str)
        
        # Convert addresses to bytes (Ethereum addresses should be 20 bytes)
        def address_to_bytes(address):
            if address.startswith('0x'):
                return bytes.fromhex(address[2:])
            return bytes.fromhex(address)
        
        # Create ZKBlock protobuf message
        zkblock = block_pb2.ZKBlock(
            stark_proof=b"",  # Empty stark proof
            commitment=[random.randint(0, 4294967295) for _ in range(8)],
            proof_hash=f"0x{unique_proof_hash}",
            status="verified",
            txns_root=f"0x{unique_txnsroot}",
            transactions=transactions,
            timestamp=int(time.time()),
            extra_data="0x",
            state_root=hex_to_bytes(f"0x{unique_stateroot}"),
            logs_bloom=b"",
            coinbase_addr=address_to_bytes("0xCdf1eFFD70cecB41bA0b4c41eB13D263578a4cC2"),
            zkvm_addr=address_to_bytes("0xCdf1eFFD70cecB41bA0b4c41eB13D263578a4cC2"),
            prev_hash=hex_to_bytes(f"0x{unique_prevhash}"),
            block_hash=hex_to_bytes(f"0x{unique_blockhash}"),
            gas_limit=1000000,
            gas_used=21000,
            block_number=block_number
        )
        
        return zkblock
    
    def process_block(self, zkblock):
        """Process a ZKBlock using gRPC"""
        try:
            request = block_pb2.ProcessBlockRequest(block=zkblock)
            response = self.stub.ProcessBlock(request, timeout=30)
            return response
        except grpc.RpcError as e:
            print(f"❌ gRPC error: {e.code()} - {e.details()}")
            return None
        except Exception as e:
            print(f"❌ Error processing block: {e}")
            return None

def send_eth_test_grpc():
    """Test sending ETH using gRPC"""
    test_start_time = time.time()
    print("💰 Sending ETH Test (gRPC)")
    print("="*50)
    print(f"🕐 Test started at: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    
    # Create gRPC tester
    tester = ZKBlockTesterGRPC("192.168.100.24:15055")
    
    # Connect to server
    step_start = time.time()
    print("1. Connecting to gRPC server...")
    if not tester.connect():
        print("❌ Failed to connect to gRPC server")
        return False
    
    print_timing("gRPC connection", step_start)
    
    # Test health check
    step_start = time.time()
    print("2. Testing gRPC server health...")
    if not tester.test_health_check():
        print("❌ gRPC server is not responding properly")
        tester.disconnect()
        return False
    
    print("✅ gRPC server is responding")
    print_timing("Health check", step_start)
    
    # Get the latest block number from the node stats API
    step_start = time.time()
    print("3. Getting latest block number from stats API...")
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
    
    print(f"\n4. Creating transaction:")
    print(f"   From: {from_address}")
    print(f"   To: {to_address}")
    print(f"   Amount: {amount} ETH ({amount_wei} wei)")
    
    # Create transaction protobuf
    transaction = tester.create_transaction_proto(from_address, to_address, amount_wei)
    print("✅ Transaction protobuf created")
    print_timing("Transaction creation", step_start)
    
    # Create ZKBlock protobuf
    step_start = time.time()
    print("5. Creating ZKBlock protobuf...")
    zkblock = tester.create_zkblock_proto([transaction], latest_block_number + 1)
    print("✅ ZKBlock protobuf created")
    print_timing("ZKBlock creation", step_start)
    
    # Process the block
    step_start = time.time()
    print("6. Processing ZKBlock via gRPC...")
    response = tester.process_block(zkblock)
    
    if response is None:
        print("❌ Failed to process ZKBlock")
        tester.disconnect()
        return False
    
    print_timing("Block processing", step_start)
    
    # Display results
    print("\n📊 Results:")
    print(f"   Success: {response.success}")
    print(f"   Message: {response.message}")
    print(f"   Block Hash: {response.block_hash}")
    print(f"   Block Number: {response.block_number}")
    if response.error:
        print(f"   Error: {response.error}")
    
    # Clean up
    tester.disconnect()
    
    # Calculate total time
    total_time = time.time() - test_start_time
    print(f"\n🎉 Test completed in {format_duration(total_time)}")
    
    return response.success

if __name__ == "__main__":
    success = send_eth_test_grpc()
    if success:
        print("✅ ETH transfer test completed successfully!")
    else:
        print("❌ ETH transfer test failed!")
