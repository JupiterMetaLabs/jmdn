const TransactionsPage = () => {
    const { useParams } = ReactRouterDOM;
    const params = useParams();
    const navigate = ReactRouterDOM.useNavigate();
    
    const [transactions, setTransactions] = React.useState([]);
    const [isLoading, setIsLoading] = React.useState(true);
    const [selectedTransaction, setSelectedTransaction] = React.useState(null);
    const [offset, setOffset] = React.useState(0);
    const [limit, setLimit] = React.useState(20);
    const [total, setTotal] = React.useState(0);
    
    // Function to fetch transactions
    const fetchTransactions = async () => {
      try {
        setIsLoading(true);
        const response = await fetch(`/api/transactions?offset=${offset}&limit=${limit}`);
        
        if (!response.ok) {
          throw new Error(`HTTP error! status: ${response.status}`);
        }
        
        const data = await response.json();
        setTransactions(data.transactions || []);
        setTotal(data.total || 0);
        setIsLoading(false);
      } catch (error) {
        console.error('Error fetching transactions:', error);
        setIsLoading(false);
      }
    };
    
    // Function to fetch a specific transaction
    const fetchTransaction = async (hash) => {
      try {
        setIsLoading(true);
        const response = await fetch(`/api/transactions/${hash}`);
        
        if (!response.ok) {
          if (response.status === 404) {
            navigate('/not-found', { replace: true });
            return;
          }
          throw new Error(`HTTP error! status: ${response.status}`);
        }
        
        const data = await response.json();
        setSelectedTransaction(data);
        setIsLoading(false);
      } catch (error) {
        console.error('Error fetching transaction:', error);
        setIsLoading(false);
      }
    };
    
    // Initial data load
    React.useEffect(() => {
      // If hash parameter exists, fetch specific transaction
      if (params.hash) {
        fetchTransaction(params.hash);
      } else {
        // Otherwise fetch transaction list
        fetchTransactions();
      }
    }, [params.hash, offset, limit]);
    
    const handleTransactionClick = (transaction) => {
      navigate(`/transactions/${transaction.hash}`);
    };
    
    const handleBackToList = () => {
      navigate('/transactions');
    };
    
    // Detail view for a specific transaction
    if (params.hash || selectedTransaction) {
      return (
        <div className="container">
          <TransactionDetail 
            transaction={selectedTransaction} 
            onBack={handleBackToList}
          />
        </div>
      );
    }
    
    // List view of all transactions
    return (
      <div className="container">
        <div className="card">
          <div className="card-header">
            <h5 className="mb-0">All Transactions</h5>
          </div>
          <div className="card-body">
            <TransactionTable 
              transactions={transactions} 
              onTransactionClick={handleTransactionClick}
              isLoading={isLoading}
              showPagination={true}
            />
          </div>
        </div>
      </div>
    );
  };