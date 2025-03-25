const Formatters = {
    // Format timestamp to readable date
    formatDate: (timestamp) => {
      if (!timestamp) return 'N/A';
      
      const date = new Date(timestamp);
      return date.toLocaleString();
    },
    
    // Format address to shorter version with ellipsis
    shortenAddress: (address, start = 6, end = 4) => {
      if (!address) return 'N/A';
      if (address.length <= start + end) return address;
      
      return `${address.slice(0, start)}...${address.slice(-end)}`;
    },
    
    // Format value to ETH with proper decimals
    formatValue: (value) => {
      if (!value) return '0';
      
      // If value is in wei (large number), convert to ETH
      if (value.length > 10) {
        const valueInEth = parseInt(value) / 1e18;
        return `${valueInEth.toFixed(6)} ETH`;
      }
      
      return value;
    },
    
    // Format transaction type
    formatTransactionType: (type) => {
      if (!type) return 'Unknown';
      
      switch (type.toLowerCase()) {
        case 'eip1559':
          return 'EIP-1559';
        case 'legacy':
          return 'Legacy';
        default:
          return type.charAt(0).toUpperCase() + type.slice(1);
      }
    },
    
    // Format gas parameters
    formatGas: (gas) => {
      if (!gas) return 'N/A';
      return parseInt(gas).toLocaleString();
    }
  };