#!/usr/bin/env python3
"""
Simple script to generate Ethereum address and DID.
"""

import secrets
import hashlib

def generate_address_and_did():
    """Generate a simple address and DID."""
    # Generate random private key
    private_key = secrets.token_hex(32)
    
    # Create address from private key hash
    hash_obj = hashlib.sha256(private_key.encode())
    address_hash = hash_obj.hexdigest()[:40]
    address = "0x" + address_hash
    
    # Generate DID
    did = f"did:jmdt:superj:{address}"
    
    return address, did

def main():
    print("Generating Address and DID...")
    print("=" * 40)
    
    address, did = generate_address_and_did()
    
    print(f"Address: {address}")
    print(f"DID:     {did}")
    
    # Show in Go app format
    print(f"\nGo app format:")
    print(f"DID: {did}, PublicKey: {address}")

if __name__ == "__main__":
    main()
