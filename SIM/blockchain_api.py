import argparse
import json
import sys
import requests
from datetime import datetime
from tabulate import tabulate
from colorama import init, Fore, Style

# Initialize colorama for colored terminal output
init()

class BlockchainAPIClient:
    """Simplified client for accessing P2P-Communication blockchain APIs"""
    
    def __init__(self, base_url):
        self.base_url = base_url.rstrip('/')
        self.session = requests.Session()
    
    def _make_request(self, endpoint, params=None):
        """Make an API request with improved error handling"""
        url = f"{self.base_url}/{endpoint.lstrip('/')}"
        try:
            response = self.session.get(url, params=params, timeout=10)
            
            # Check status code
            if response.status_code != 200:
                print(f"{Fore.YELLOW}Server returned non-200 status code: {response.status_code}{Style.RESET_ALL}")
                return {"error": f"Server returned status code {response.status_code}"}
            
            # Try to parse JSON
            try:
                data = response.json()
                return data
            except json.JSONDecodeError as e:
                print(f"{Fore.RED}Failed to parse JSON response: {str(e)}{Style.RESET_ALL}")
                print(f"Response text (first 100 chars): {response.text[:100]}")
                return {"error": f"Invalid JSON: {str(e)}"}
                
        except requests.RequestException as e:
            print(f"{Fore.RED}Request error: {str(e)}{Style.RESET_ALL}")
            return {"error": f"Request failed: {str(e)}"}
    
    def list_transactions(self, limit=100):
        """List transactions"""
        return self._make_request("api/transactions", params={"limit": limit})
    
    def get_transaction(self, tx_hash):
        """Get a specific transaction by hash"""
        return self._make_request(f"api/transactions/{tx_hash}")
    
    def list_keys(self, prefix=None, limit=100):
        """List database keys with optional prefix"""
        params = {"limit": limit}
        if prefix:
            params["prefix"] = prefix
        return self._make_request("api/keys", params=params)


def format_timestamp(timestamp):
    """Format timestamp for display"""
    if isinstance(timestamp, str):
        try:
            dt = datetime.fromisoformat(timestamp.replace('Z', '+00:00'))
            return dt.strftime('%Y-%m-%d %H:%M:%S')
        except ValueError:
            return timestamp
    return timestamp


def print_table(data, headers):
    """Print data as a formatted table"""
    if not data:
        print(f"{Fore.YELLOW}No data available{Style.RESET_ALL}")
        return
    
    print(tabulate(data, headers=headers, tablefmt="pretty"))


def print_json(data):
    """Print JSON data with indentation and color"""
    if not data:
        print(f"{Fore.YELLOW}No data available{Style.RESET_ALL}")
        return
    
    json_str = json.dumps(data, indent=2)
    # Basic syntax highlighting for JSON
    json_str = json_str.replace('":', f'"{Fore.GREEN}:{Style.RESET_ALL}')
    print(json_str)


def handle_list_transactions(args):
    """Handle listing transactions command"""
    client = BlockchainAPIClient(args.url)
    
    print(f"{Fore.CYAN}Fetching transactions from {args.url}/api/transactions...{Style.RESET_ALL}")
    
    result = client.list_transactions(limit=args.limit)
    
    if "error" in result:
        print(f"{Fore.RED}Error: {result['error']}{Style.RESET_ALL}")
        return
        
    # Check if result has the expected structure
    if not isinstance(result, dict):
        print(f"{Fore.RED}Unexpected response format: {type(result)}{Style.RESET_ALL}")
        print_json(result)
        return
    
    transactions = result.get('transactions', [])
    count = result.get('count', 0)
    
    if count == 0 and isinstance(transactions, list) and len(transactions) > 0:
        count = len(transactions)
    
    print(f"{Fore.CYAN}Transactions ({count}):{Style.RESET_ALL}")
    
    if not transactions:
        print(f"{Fore.YELLOW}No transactions found{Style.RESET_ALL}")
        return
    
    # Create table data
    table_data = []
    for tx in transactions:
        if not isinstance(tx, dict):
            continue
            
        # Get hash with fallback options
        tx_hash = tx.get('hash', tx.get('transaction_hash', ''))
        if tx_hash and len(tx_hash) > 16:
            tx_hash = tx_hash[:16] + '...'
            
        # Get addresses with fallback options    
        from_addr = tx.get('from', '')
        if from_addr and len(from_addr) > 16:
            from_addr = from_addr[:16] + '...'
            
        to_addr = tx.get('to', '')
        if to_addr and len(to_addr) > 16:
            to_addr = to_addr[:16] + '...'
        
        # Build table row
        row = [
            tx_hash,
            from_addr,
            to_addr,
            tx.get('value', ''),
            tx.get('type', ''),
            format_timestamp(tx.get('timestamp', ''))
        ]
        table_data.append(row)
    
    headers = ["Hash", "From", "To", "Value", "Type", "Timestamp"]
    print_table(table_data, headers)
    
    if args.full:
        print(f"\n{Fore.CYAN}Full Transaction Data:{Style.RESET_ALL}")
        print_json(transactions)


def handle_get_transaction(args):
    """Handle getting a specific transaction command"""
    client = BlockchainAPIClient(args.url)
    
    print(f"{Fore.CYAN}Fetching transaction {args.hash} from {args.url}/api/transactions/{args.hash}...{Style.RESET_ALL}")
    
    tx = client.get_transaction(args.hash)
    
    if "error" in tx:
        print(f"{Fore.RED}Error: {tx['error']}{Style.RESET_ALL}")
        return
    
    print(f"{Fore.CYAN}Transaction Details:{Style.RESET_ALL}")
    print(f"Hash: {tx.get('hash', args.hash)}")
    print(f"Type: {tx.get('type', 'unknown')}")
    print(f"From: {tx.get('from', 'unknown')}")
    print(f"To: {tx.get('to', 'unknown')}")
    print(f"Value: {tx.get('value', 'unknown')}")
    print(f"Timestamp: {format_timestamp(tx.get('timestamp', 'unknown'))}")
    
    print(f"\n{Fore.CYAN}Transaction Parameters:{Style.RESET_ALL}")
    print(f"Chain ID: {tx.get('chain_id', 'unknown')}")
    print(f"Nonce: {tx.get('nonce', 'unknown')}")
    print(f"Gas Limit: {tx.get('gas_limit', 'unknown')}")
    print(f"Gas Price: {tx.get('gas_price', 'unknown')}")
    
    if tx.get('type', '').lower() == 'eip1559':
        print(f"Max Fee: {tx.get('max_fee', 'unknown')}")
        print(f"Max Priority Fee: {tx.get('max_priority_fee', 'unknown')}")
    
    if tx.get('data', ''):
        print(f"\n{Fore.CYAN}Transaction Data:{Style.RESET_ALL}")
        print(tx.get('data', ''))
    
    if 'raw_data' in tx and tx['raw_data']:
        print(f"\n{Fore.CYAN}Raw Transaction Data:{Style.RESET_ALL}")
        print_json(tx['raw_data'])


def handle_list_keys(args):
    """Handle listing keys command"""
    client = BlockchainAPIClient(args.url)
    
    print(f"{Fore.CYAN}Fetching keys with prefix '{args.prefix}' from {args.url}/api/keys...{Style.RESET_ALL}")
    
    result = client.list_keys(prefix=args.prefix, limit=args.limit)
    
    if "error" in result:
        print(f"{Fore.RED}Error: {result['error']}{Style.RESET_ALL}")
        return
    
    # Check if result has the expected structure
    if not isinstance(result, dict):
        print(f"{Fore.RED}Unexpected response format: {type(result)}{Style.RESET_ALL}")
        print_json(result)
        return
    
    keys = result.get('keys', [])
    count = result.get('count', len(keys))
    prefix = result.get('prefix', args.prefix)
    
    print(f"{Fore.CYAN}Keys with prefix '{prefix}' ({count}):{Style.RESET_ALL}")
    
    if not keys:
        print(f"{Fore.YELLOW}No keys found{Style.RESET_ALL}")
        return
    
    for i, key in enumerate(keys, 1):
        print(f"{i}. {key}")


def main():
    parser = argparse.ArgumentParser(description="Simplified P2P-Communication API Client")
    subparsers = parser.add_subparsers(dest="command", help="Command to execute")
    
    # List transactions command
    list_tx_parser = subparsers.add_parser("list-transactions", help="List transactions")
    list_tx_parser.add_argument("--url", default="http://localhost:8090", help="API base URL")
    list_tx_parser.add_argument("--limit", type=int, default=100, help="Maximum number of transactions to list")
    list_tx_parser.add_argument("--full", action="store_true", help="Show full transaction details")
    
    # Get transaction command
    get_tx_parser = subparsers.add_parser("get-transaction", help="Get a specific transaction")
    get_tx_parser.add_argument("hash", help="Transaction hash")
    get_tx_parser.add_argument("--url", default="http://localhost:8090", help="API base URL")
    
    # List keys command
    list_keys_parser = subparsers.add_parser("list-keys", help="List database keys")
    list_keys_parser.add_argument("--url", default="http://localhost:8090", help="API base URL")
    list_keys_parser.add_argument("--prefix", default="tx:", help="Key prefix to filter by")
    list_keys_parser.add_argument("--limit", type=int, default=100, help="Maximum number of keys to list")
    
    args = parser.parse_args()
    
    if not args.command:
        parser.print_help()
        return
    
    # Execute the appropriate command
    command_handlers = {
        "list-transactions": handle_list_transactions,
        "get-transaction": handle_get_transaction,
        "list-keys": handle_list_keys
    }
    
    handler = command_handlers.get(args.command)
    if handler:
        handler(args)
    else:
        print(f"{Fore.RED}Unknown command: {args.command}{Style.RESET_ALL}")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\nOperation canceled by user")
        sys.exit(0)