const BlockDetail = ({ block, onBack }) => {
    if (!block) {
      return (
        <div className="loader">
          <div className="spinner-border text-primary" role="status">
            <span className="visually-hidden">Loading...</span>
          </div>
          <p className="mt-2">Loading block details...</p>
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
          <h5 className="mb-0">Block Details</h5>
          {onBack && (
            <button className="btn btn-sm btn-outline-primary" onClick={onBack}>
              <i className="bi bi-arrow-left me-1"></i> Back
            </button>
          )}
        </div>
        <div className="card-body">
          <table className="detail-table">
            <tbody>
              {renderDetailRow("ID", block.id)}
              {renderDetailRow("Type", block.type, Formatters.formatTransactionType)}
              {renderDetailRow("Nonce", block.nonce)}
              {renderDetailRow("Sender", block.sender)}
              {renderDetailRow("Timestamp", block.timestamp, Formatters.formatDate)}
              {renderDetailRow("Hops", block.hops)}
              {block.transactionHash && renderDetailRow("Transaction Hash", block.transactionHash)}
              {block.from && renderDetailRow("From", block.from)}
              {block.to && renderDetailRow("To", block.to)}
              {block.value && renderDetailRow("Value", block.value, Formatters.formatValue)}
            </tbody>
          </table>
          
          {block.data && Object.keys(block.data).length > 0 && (
            <div className="mt-4">
              <h6>Block Data</h6>
              <pre className="border rounded p-3 bg-light">
                {JSON.stringify(block.data, null, 2)}
              </pre>
            </div>
          )}
          
          {block.rawData && Object.keys(block.rawData).length > 0 && (
            <div className="mt-4">
              <h6>Raw Data</h6>
              <pre className="border rounded p-3 bg-light">
                {JSON.stringify(block.rawData, null, 2)}
              </pre>
            </div>
          )}
        </div>
      </div>
    );
  };