const Dashboard = () => {
  const navigate = ReactRouterDOM.useNavigate();
  const [isLoading, setIsLoading] = React.useState(true);
  const [dashboardData, setDashboardData] = React.useState({
    stats: {
      block_count: 0,
      transaction_count: 0,
      node_count: 0,
      total_amount: 0
    },
    latest_transactions: [],
    latest_blocks: []
  });
  const [selectedTransaction, setSelectedTransaction] = React.useState(null);
  const [wsClient, setWsClient] = React.useState(null);
  
  // Function to fetch dashboard data
  const fetchDashboardData = async () => {
    try {
      setIsLoading(true);
      const response = await fetch('/api/dashboard');
      
      if (!response.ok) {
        throw new Error('HTTP error! status: ' + response.status);
      }
      
      const data = await response.json();
      setDashboardData(data || {
        stats: {
          block_count: 0,
          transaction_count: 0,
          node_count: 0,
          total_amount: 0
        },
        latest_transactions: [],
        latest_blocks: []
      });
      setIsLoading(false);
    } catch (error) {
      console.error('Error fetching dashboard data:', error);
      setIsLoading(false);
    }
  };
  
  // Handle WebSocket connection
  React.useEffect(() => {
    // Determine WebSocket URL (production vs development)
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${wsProtocol}//${window.location.host}/ws`;
    
    // Handle incoming WebSocket messages
    const handleMessage = (message) => {
      console.log('Received WebSocket message:', message);
      
      // Update the dashboard when we receive a dashboard update
      if (message.type === 'dashboard') {
        setDashboardData(message.data || {
          stats: {
            block_count: 0,
            transaction_count: 0,
            node_count: 0,
            total_amount: 0
          },
          latest_transactions: [],
          latest_blocks: []
        });
      }
      
      // Update stats when we receive a stats update
      if (message.type === 'stats') {
        setDashboardData(function(prevData) {
          if (!prevData) return {
            stats: message.data,
            latest_transactions: [],
            latest_blocks: []
          };
          
          return {
            ...prevData,
            stats: message.data
          };
        });
      }
      
      // Add transaction to latest transactions when we receive a transaction
      if (message.type === 'transaction') {
        const newTx = message.data;
        setDashboardData(function(prevData) {
          if (!prevData) return {
            stats: {
              block_count: 0,
              transaction_count: 0,
              node_count: 0,
              total_amount: 0
            },
            latest_transactions: [newTx],
            latest_blocks: []
          };
          
          // Copy the latest transactions and add the new one at the top
          const updatedTransactions = [
            newTx,
            ...(prevData.latest_transactions || []).slice(0, 9)
          ];
          
          return {
            ...prevData,
            latest_transactions: updatedTransactions
          };
        });
      }
      
      // Add block to latest blocks when we receive a block
      if (message.type === 'block') {
        const newBlock = message.data;
        setDashboardData(function(prevData) {
          if (!prevData) return {
            stats: {
              block_count: 0,
              transaction_count: 0,
              node_count: 0,
              total_amount: 0
            },
            latest_transactions: [],
            latest_blocks: [newBlock]
          };
          
          // Copy the latest blocks and add the new one at the top
          const updatedBlocks = [
            newBlock,
            ...(prevData.latest_blocks || []).slice(0, 9)
          ];
          
          return {
            ...prevData,
            latest_blocks: updatedBlocks
          };
        });
      }
    };
    
    // Initialize WebSocket client
    const client = new WebSocketClient(
      wsUrl, 
      handleMessage,
      function() { console.log('WebSocket connected'); },
      function() { console.log('WebSocket disconnected'); }
    );
    
    client.connect();
    setWsClient(client);
    
    // Initial data load
    fetchDashboardData();
    
    // Cleanup on unmount
    return function() {
      if (client) {
        client.disconnect();
      }
    };
  }, []);
  
  // Request refresh dashboard data periodically
  React.useEffect(() => {
    if (!wsClient) return;
    
    const interval = setInterval(() => {
      wsClient.send({ type: 'refresh_dashboard' });
    }, 30000); // Every 30 seconds
    
    return function() { clearInterval(interval); };
  }, [wsClient]);
  
  const handleTransactionClick = (transaction) => {
    setSelectedTransaction(transaction);
  };
  
  const handleBackToList = () => {
    setSelectedTransaction(null);
  };
  
  const handleViewAllTransactions = () => {
    navigate('/transactions');
  };
  
  const handleViewAllBlocks = () => {
    navigate('/blocks');
  };
  
  if (isLoading) {
    return (
      <div className="container mt-5">
        <div className="row justify-content-center">
          <div className="col-md-8 text-center">
            <div className="spinner-border text-primary" role="status">
              <span className="visually-hidden">Loading...</span>
            </div>
            <p className="mt-3">Loading blockchain data...</p>
          </div>
        </div>
      </div>
    );
  }
  
  // Display transaction details if a transaction is selected
  if (selectedTransaction) {
    return (
      <div className="container">
        <TransactionDetail 
          transaction={selectedTransaction} 
          onBack={handleBackToList}
        />
      </div>
    );
  }
  
  // Main dashboard view
  return (
    <div className="container">
      {/* Stats Section */}
      <div className="row mb-4">
        <StatCard 
          icon="box" 
          value={dashboardData.stats ? dashboardData.stats.block_count || 0 : 0} 
          label="Total Blocks" 
          color="#3498db"
        />
        <StatCard 
          icon="arrow-left-right" 
          value={dashboardData.stats ? dashboardData.stats.transaction_count || 0 : 0} 
          label="Total Transactions" 
          color="#2ecc71"
        />
        <StatCard 
          icon="pc-display" 
          value={dashboardData.stats ? dashboardData.stats.node_count || 0 : 0} 
          label="Active Nodes" 
          color="#f39c12"
        />
        <StatCard 
          icon="coin" 
          value={dashboardData.stats && dashboardData.stats.total_amount ? 
                 dashboardData.stats.total_amount.toFixed(2) : "0.00"} 
          label="Total Value" 
          color="#e74c3c"
        />
      </div>
      
      {/* Latest Transactions */}
      <div className="row mb-4">
        <div className="col-12">
          <div className="card">
            <div className="card-header d-flex justify-content-between align-items-center">
              <h5 className="mb-0">Latest Transactions</h5>
              <button 
                className="btn btn-sm btn-outline-primary" 
                onClick={handleViewAllTransactions}
              >
                View All
              </button>
            </div>
            <div className="card-body">
              <TransactionTable 
                transactions={dashboardData.latest_transactions || []} 
                onTransactionClick={handleTransactionClick}
                isLoading={false}
              />
            </div>
          </div>
        </div>
      </div>
      
      {/* Latest Blocks */}
      <div className="row mb-4">
        <div className="col-12">
          <div className="card">
            <div className="card-header d-flex justify-content-between align-items-center">
              <h5 className="mb-0">Latest Blocks</h5>
              <button 
                className="btn btn-sm btn-outline-primary" 
                onClick={handleViewAllBlocks}
              >
                View All
              </button>
            </div>
            <div className="card-body">
              <BlockTable 
                blocks={dashboardData.latest_blocks || []} 
                isLoading={false}
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  );
};