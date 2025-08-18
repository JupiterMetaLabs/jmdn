import json
import requests
from eth_account import Account
from web3 import Web3
from eth_utils import to_hex
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ed25519
from cryptography.hazmat.backends import default_backend

def sign_transaction(private_key, tx_data):
    """
    Sign a transaction with the given private key
    """
    # Initialize account
    account = Account.from_key(private_key)
    
    # Prepare the transaction
    transaction = {
        'chainId': int(tx_data['chain_id']),
        'nonce': int(tx_data['nonce']),
        'to': tx_data['to'],
        'value': int(tx_data['value']),
        'gas': int(tx_data['gas_limit']),
        'gasPrice': int(tx_data['gas_price']),
        'data': tx_data['data'] or '0x',
    }
    
    # Sign the transaction
    signed_tx = Account.sign_transaction(transaction, private_key)
    
    # Return the signed transaction components
    return {
        'from': account.address,
        'to': tx_data['to'],
        'value': tx_data['value'],
        'gas_limit': tx_data['gas_limit'],
        'gas_price': tx_data['gas_price'],
        'data': tx_data['data'],
        'v': hex(signed_tx.v),
        'r': to_hex(signed_tx.r),
        's': to_hex(signed_tx.s),
        'type': '0x0',  # Legacy transaction
        'chain_id': tx_data['chain_id'],
        'nonce': tx_data['nonce']
    }

def submit_transaction(api_url, signed_tx):
    """
    Submit a signed transaction to the node API
    """
    # Prepare the request payload
    payload = {
        'raw_tx': json.dumps(signed_tx)
    }
    
    # Make the API request
    response = requests.post(
        f"{api_url}/api/submit-raw-tx",
        headers={"Content-Type": "application/json"},
        json=payload
    )
    
    return response.json()

if __name__ == "__main__":
    # Configuration - Replace with your values
    private_key = ed25519.Ed25519PrivateKey.generate()
    PRIVATE_KEY = private_key.private_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PrivateFormat.Raw,
        encryption_algorithm=serialization.NoEncryption()
    ).hex()
    
    API_URL = "http://localhost:15050"
    
    # Transaction parameters
    tx_data = {
        'chain_id': '7000700',  # Your chain ID
        'from': 'did:jmdt:iskcon:763034B2507cF0Fdd5B47614CB3295C029348c39',
        'nonce': '12',  # Must be the next nonce for the from address
        'to': 'did:jmdt:superj:01F0bAD1b881f7D465c264A52E02E8BdA5ea078B',
        'value': '10000000000000000000',  # 10 tokens (adjust as needed)
        'data': '',  # Empty for simple transfers
        'gas_limit': '21000',  # Standard gas limit for simple transfers
        'gas_price': '50000000000',  # 50 Gwei (adjust as needed)
    }
    
    try:
        # Sign the transaction
        print("Signing transaction...")
        signed_tx = sign_transaction(PRIVATE_KEY, tx_data)
        
        # Submit the transaction
        print("Submitting transaction...")
        result = submit_transaction(API_URL, signed_tx)
        
        print("\nTransaction submitted successfully!")
        print("Response:", json.dumps(result, indent=2))
        
    except Exception as e:
        print(f"Error: {str(e)}")
        if hasattr(e, 'response') and hasattr(e.response, 'text'):
            print("Server response:", e.response.text)
