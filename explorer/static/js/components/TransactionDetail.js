const TransactionDetail = ({ transaction, onBack }) => {
    if (!transaction) {
      return (
        <div className="loader">
          <div className="spinner-border text-primary" role="status">
            <span className="visually-hidden">Loading...</span>
          </div>
          <p className="mt-2">Loading transaction details...</p>
        </div>
      );
    }
    
    const renderDetailRow = (label, value, formatter = (val) => val || 'N/A') => (
      <tr>
        <th>{label}</th>
        <td>{formatter(value)}</td>
      </tr>
    );
    
    return (
      <div className="card detail-card">
        <div className="card-header d-flex justify-content-between align-items-center">
          <h5 className="mb-0">Transaction Details</h5>
          {onBack && (
            <button className="btn btn-sm btn-outline-primary" onClick={onBack}>
              <i className="bi bi-arrow-left me-1"></i> Back
            </button>
          )}
        </div>
        <div className="card-body">
          <table className="detail-table">
            <tbody>
              {renderDetailRow("Hash", transaction.hash)}
              {renderDetailRow("Type", transaction.type, Formatters.formatTransactionType)}
              {renderDetailRow("From", transaction.from)}
              {renderDetailRow("To", transaction.to)}
              {renderDetailRow("Value", transaction.value, Formatters.formatValue)}
              {renderDetailRow("Timestamp", transaction.timestamp, Formatters.formatDate)}
              {transaction.nonce && renderDetailRow("Nonce", transaction.nonce)}
              {transaction.chainId && renderDetailRow("Chain ID", transaction.chainId)}
              {transaction.gasLimit && renderDetailRow("Gas Limit", transaction.gasLimit, Formatters.formatGas)}
              {transaction.gasPrice && renderDetailRow("Gas Price", transaction.gasPrice, Formatters.formatGas)}
              {transaction.maxFee && renderDetailRow("Max Fee", transaction.maxFee, Formatters.formatGas)}
              {transaction.maxPriorityFee && renderDetailRow("Max Priority Fee", transaction.maxPriorityFee, Formatters.formatGas)}
              {transaction.data && renderDetailRow("Data", transaction.data)}
            </tbody>
          </table>
          
          {transaction.rawData && Object.keys(transaction.rawData).length > 0 && (
            <div className="mt-4">
              <h6>Raw Data</h6>
              <pre className="border rounded p-3 bg-light">
                {JSON.stringify(transaction.rawData, null, 2)}
              </pre>
            </div>
          )}
        </div>
      </div>
    );
  };