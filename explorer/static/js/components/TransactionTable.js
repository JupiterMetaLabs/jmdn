const TransactionTable = ({ transactions, onTransactionClick, isLoading, showPagination = false }) => {
    const navigate = ReactRouterDOM.useNavigate();
    const [newTxHashes, setNewTxHashes] = React.useState(new Set());
    
    // Highlight new transactions for 2 seconds
    React.useEffect(() => {
      if (transactions.length > 0) {
        const hashes = new Set();
        transactions.forEach(tx => hashes.add(tx.hash));
        setNewTxHashes(hashes);
        
        const timer = setTimeout(() => {
          setNewTxHashes(new Set());
        }, 2000);
        
        return () => clearTimeout(timer);
      }
    }, [transactions]);
    
    const handleRowClick = (tx) => {
      if (onTransactionClick) {
        onTransactionClick(tx);
      } else {
        navigate(`/transactions/${tx.hash}`);
      }
    };
    
    if (isLoading) {
      return (
        <div className="loader">
          <div className="spinner-border text-primary" role="status">
            <span className="visually-hidden">Loading...</span>
          </div>
          <p className="mt-2">Loading transactions...</p>
        </div>
      );
    }
    
    if (transactions.length === 0) {
      return (
        <div className="alert alert-info">
          No transactions found.
        </div>
      );
    }
    
    return (
      <div className="table-responsive">
        <table className="transaction-table">
          <thead>
            <tr>
              <th>Hash</th>
              <th>Type</th>
              <th>From</th>
              <th>To</th>
              <th>Value</th>
              <th>Timestamp</th>
            </tr>
          </thead>
          <tbody>
            {transactions.map(tx => (
              <tr 
                key={tx.hash} 
                onClick={() => handleRowClick(tx)}
                className={newTxHashes.has(tx.hash) ? 'new-transaction' : ''}
              >
                <td>
                  <span className="transaction-hash">{Formatters.shortenAddress(tx.hash, 8, 8)}</span>
                </td>
                <td>
                  <span className={`badge-transaction-type ${tx.type.toLowerCase()}`}>
                    {Formatters.formatTransactionType(tx.type)}
                  </span>
                </td>
                <td>
                  <span className="address" title={tx.from}>
                    {Formatters.shortenAddress(tx.from)}
                  </span>
                </td>
                <td>
                  <span className="address" title={tx.to}>
                    {Formatters.shortenAddress(tx.to)}
                  </span>
                </td>
                <td>{Formatters.formatValue(tx.value)}</td>
                <td>{Formatters.formatDate(tx.timestamp)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        
        {showPagination && (
          <nav>
            <ul className="pagination">
              <li className="page-item"><a className="page-link" href="#">Previous</a></li>
              <li className="page-item active"><a className="page-link" href="#">1</a></li>
              <li className="page-item"><a className="page-link" href="#">2</a></li>
              <li className="page-item"><a className="page-link" href="#">3</a></li>
              <li className="page-item"><a className="page-link" href="#">Next</a></li>
            </ul>
          </nav>
        )}
      </div>
    );
  };