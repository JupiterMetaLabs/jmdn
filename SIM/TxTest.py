# import json
# import requests
# import time
# from eth_account import Account
# from web3 import Web3
# from eth_utils import to_hex, to_checksum_address
# from eth_keys import keys
# from eth_utils import keccak

def generate_ethereum_keypair():
    """Generate an Ethereum compatible keypair"""
    account = Account.create()
    return {
        'private_key': account.key.hex(),
        'address': account.address
    }

def sign_transaction(private_key_hex, tx_data):
    """
    Sign a transaction and prepare the full payload for the API.
    """
    # Ensure private key is in the correct format
    if private_key_hex.startswith('0x'):
        private_key_hex = private_key_hex[2:]
    
    # Initialize account
    account = Account.from_key('0x' + private_key_hex)
    
    # Extract and validate 'to' address from DID format
    to_address = tx_data['to'].split(':')[-1]
    if not to_address.startswith('0x'):
        to_address = '0x' + to_address

    print("To Address: ", to_address)    
    # Prepare the transaction with proper types
    transaction = {
        'chainId': int(tx_data['chain_id']),
        'nonce': int(tx_data['nonce']),
        'to': to_address,
        'value': int(tx_data['value']),
        'gas': int(tx_data['gas_limit']),
        'gasPrice': int(tx_data['gas_price']),
        'data': tx_data['data'] or '0x',
    }
    
    # Sign the transaction
    signed_tx = Account.sign_transaction(transaction, '0x' + private_key_hex)
    print("Signed Transaction (raw components): ", signed_tx)
    
    # Construct the JSON payload expected by the Go server.
    # This should match the `config.ZKBlockTransaction` struct that the
    # server's `/api/submit-raw-tx` endpoint binds to.
    # Based on the Go code, the server expects a full transaction object in JSON,
    # not just the raw RLP-encoded transaction.
    api_payload = {
        "chain_id": tx_data['chain_id'],
        "nonce": tx_data['nonce'],
        "to": tx_data['to'],
        "from": tx_data['from'],
        "value": tx_data['value'],
        "gas_limit": tx_data['gas_limit'],
        "gas_price": tx_data['gas_price'],
        "data": tx_data['data'],
        "v": str(signed_tx.v),  # Convert to decimal string
        "r": str(signed_tx.r),  # Convert to decimal string
        "s": str(signed_tx.s),  # Convert to decimal string
        "type": "0",  # Legacy transaction
        "timestamp": str(int(time.time())),
    }
        
    return api_payload

# def submit_transaction(api_url, tx_payload):
#     """
#     Submit a signed transaction payload to the node API
    
#     Args:
#         api_url (str): Base URL of the node API
#         tx_payload (dict): Dictionary containing the full transaction payload
    
#     Returns:
#         dict: Response from the node API
#     """
#     print(f"\nSubmitting transaction to {api_url}...")
#     print(f"Payload: {json.dumps(tx_payload, indent=2)}")
    
#     try:
#         # Make the API request
#         response = requests.post(
#             f"{api_url}/api/submit-raw-tx",
#             headers={
#                 "Content-Type": "application/json",
#                 "Accept": "application/json"
#             },
#             json=tx_payload,  # Send the payload directly
#             timeout=30
#         )
        
#         # Check for HTTP errors
#         response.raise_for_status()
        
#         # Return the JSON response
#         return response.json()
        
#     except requests.exceptions.RequestException as e:
#         error_msg = str(e)
#         if hasattr(e, 'response') and e.response is not None:
#             error_msg += f"\nStatus Code: {e.response.status_code}"
#             try:
#                 error_msg += f"\nResponse: {e.response.text}"
#             except:
#                 pass
#         raise Exception(f"Failed to submit transaction: {error_msg}")

import grpc
import json
import time

# NOTE: Generate these files by running the following command from the project root:
# python -m grpc_tools.protoc -I. --python_out=. --grpc_python_out=. gETH/proto/gETH.proto
import gETH_pb2
import gETH_pb2_grpc


def run_grpc(tx_data):
    """Connects to the gRPC server and sends a raw transaction."""

    # 1. Construct a sample transaction based on the Go ZKBlockTransaction struct.
    #    Replace with your actual transaction data.
    ## get data form the function 


    # 2. Serialize the dictionary to a JSON string, then encode to bytes.
    signed_tx_bytes = json.dumps(tx_data).encode('utf-8')

    # 3. Connect to the gRPC server.
    #    Replace 'localhost:50051' with your gRPC server's address.
    server_address = '192.168.100.139:15055'
    print(f"Connecting to {server_address}...")

    try:
        with grpc.insecure_channel(server_address) as channel:
            stub = gETH_pb2_grpc.ChainStub(channel)

            # 4. Create the request object.
            request = gETH_pb2.SendRawTxReq(signed_tx=signed_tx_bytes)

            # 5. Call the RPC and get the response.
            print("Sending raw transaction...")
            response = stub.SendRawTransaction(request)

            if response.error:
                print(f"Error from server: {response.error}")
            else:
                tx_hash_hex = '0x' + response.tx_hash.hex()
                print(f"Successfully sent transaction. Hash: {tx_hash_hex}")

    except grpc.RpcError as e:
        print(f"gRPC connection failed: {e.code()} - {e.details()}")
    except Exception as e:
        print(f"An unexpected error occurred: {e}")
    


if __name__ == "__main__":
    # Configuration
    API_URL = "http://localhost:15050"

    try: 
        from eth_account import Account
        Account.enable_unaudited_hdwallet_features()
        # Generate or use existing keypair
        mnemonic = "hawk moral razor cabin magnet prefer mask cram goat auto certain wrap"
        account = Account.from_mnemonic(mnemonic)
        keypair = {
            'private_key': account.key.hex(),
            'address': account.address
        }
        print(f"Generated new Ethereum account:")
        print(f"Private Key: 0x{keypair['private_key']}")
        print(f"Address: {keypair['address']}")
        print(f"DID: did:jmdt:superj:{keypair['address'].lower()}")
        
        # Transaction parameters
        tx_data = {
            'chain_id': '8000800',  # Your chain ID
            'from': f'did:jmdt:superj:{keypair["address"]}',
            'nonce': '0',  # Start with 0 for new accounts
            'to': 'did:jmdt:superj:01F0bAD1b881f7D465c264A52E02E8BdA5ea078B',
            'value': '4',  # 4 tokens in wei (4 * 10^18)
            'data': '0x',  # Empty for simple transfers
            'gas_limit': '21000',  # Standard gas limit for simple transfers
            'gas_price': '2000',  # 20 Gwei (20 * 10^9)
        }
        
        print("\nTransaction Data:")
        print(json.dumps(tx_data, indent=2))
        
        try:
            print("\nSigning transaction...")
            tx_payload = sign_transaction(keypair['private_key'], tx_data)
            print(f"API Payload prepared: {json.dumps(tx_payload, indent=2)}")
            
            run_grpc(tx_payload)

        except ValueError as ve:
            print(f"\nValidation Error: {str(ve)}")
        except Exception as e:
            print(f"\nError during transaction processing: {str(e)}")
            
    except Exception as e:
        print(f"\nFatal Error: {str(e)}")
        import traceback
        traceback.print_exc()