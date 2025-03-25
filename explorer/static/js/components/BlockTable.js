const BlockTable = ({ blocks, onBlockClick, isLoading, showPagination = false }) => {
    const navigate = ReactRouterDOM.useNavigate();
    
    const handleRowClick = (block) => {
      if (onBlockClick) {
        onBlockClick(block);
      } else {
        navigate(`/blocks/${block.id}`);
      }
    };
    
    if (isLoading) {
      return (
        <div className="loader">
          <div className="spinner-border text-primary" role="status">
            <span className="visually-hidden">Loading...</span>
          </div>
          <p className="mt-2">Loading blocks...</p>
        </div>
      );
    }
    
    if (blocks.length === 0) {
      return (
        <div className="alert alert-info">
          No blocks found.
        </div>
      );
    }
    
    return (
      <div className="table-responsive">
        <table className="block-table">
          <thead>
            <tr>
              <th>ID</th>
              <th>Type</th>
              <th>Nonce</th>
              <th>Sender</th>
              <th>Timestamp</th>
            </tr>
          </thead>
          <tbody>
            {blocks.map(block => (
              <tr key={block.id} onClick={() => handleRowClick(block)}>
                <td>
                  <span className="block-id">{Formatters.shortenAddress(block.id, 8, 8)}</span>
                </td>
                <td>
                  <span className={`badge-transaction-type ${block.type.toLowerCase()}`}>
                    {Formatters.formatTransactionType(block.type)}
                  </span>
                </td>
                <td>{block.nonce}</td>
                <td>
                  <span className="address" title={block.sender}>
                    {Formatters.shortenAddress(block.sender)}
                  </span>
                </td>
                <td>{Formatters.formatDate(block.timestamp)}</td>
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