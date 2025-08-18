import json
import requests
import time
from eth_account import Account
from web3 import Web3
from eth_utils import to_hex, to_checksum_address
from eth_keys import keys
from eth_utils import keccak

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
# In the sign_transaction function, update the api_payload to use decimal strings:
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

def submit_transaction(api_url, tx_payload):
    """
    Submit a signed transaction payload to the node API
    
    Args:
        api_url (str): Base URL of the node API
        tx_payload (dict): Dictionary containing the full transaction payload
    
    Returns:
        dict: Response from the node API
    """
    print(f"\nSubmitting transaction to {api_url}...")
    print(f"Payload: {json.dumps(tx_payload, indent=2)}")
    
    try:
        # Make the API request
        response = requests.post(
            f"{api_url}/api/submit-raw-tx",
            headers={
                "Content-Type": "application/json",
                "Accept": "application/json"
            },
            json=tx_payload,  # Send the payload directly
            timeout=30
        )
        
        # Check for HTTP errors
        response.raise_for_status()
        
        # Return the JSON response
        return response.json()
        
    except requests.exceptions.RequestException as e:
        error_msg = str(e)
        if hasattr(e, 'response') and e.response is not None:
            error_msg += f"\nStatus Code: {e.response.status_code}"
            try:
                error_msg += f"\nResponse: {e.response.text}"
            except:
                pass
        raise Exception(f"Failed to submit transaction: {error_msg}")
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
            'chain_id': '7000700',  # Your chain ID
            'from': f'did:jmdt:superj:{keypair["address"]}',
            'nonce': '0',  # Start with 0 for new accounts
            'to': 'did:jmdt:superj:01F0bAD1b881f7D465c264A52E02E8BdA5ea078B',
            'value': '4000000000000000000',  # 4 tokens in wei (4 * 10^18)
            'data': '0x',  # Empty for simple transfers
            'gas_limit': '21000',  # Standard gas limit for simple transfers
            'gas_price': '20000000000',  # 20 Gwei (20 * 10^9)
        }
        
        print("\nTransaction Data:")
        print(json.dumps(tx_data, indent=2))
        
        try:
            print("\nSigning transaction...")
            tx_payload = sign_transaction(keypair['private_key'], tx_data)
            print(f"API Payload prepared: {json.dumps(tx_payload, indent=2)}")
            
            # Submit the transaction
            result = submit_transaction(API_URL, tx_payload)
            
            print("\nTransaction submitted successfully!")
            print("Result:", json.dumps(result, indent=2))
            
        except ValueError as ve:
            print(f"\nValidation Error: {str(ve)}")
        except Exception as e:
            print(f"\nError during transaction processing: {str(e)}")
            
    except Exception as e:
        print(f"\nFatal Error: {str(e)}")
        import traceback
        traceback.print_exc()