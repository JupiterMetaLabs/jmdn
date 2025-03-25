const BlocksPage = () => {
    const { useParams } = ReactRouterDOM;
    const params = useParams();
    const navigate = ReactRouterDOM.useNavigate();
    
    const [blocks, setBlocks] = React.useState([]);
    const [isLoading, setIsLoading] = React.useState(true);
    const [selectedBlock, setSelectedBlock] = React.useState(null);
    const [offset, setOffset] = React.useState(0);
    const [limit, setLimit] = React.useState(20);
    const [total, setTotal] = React.useState(0);
    
    // Function to fetch blocks
    const fetchBlocks = async () => {
      try {
        setIsLoading(true);
        const response = await fetch(`/api/blocks?offset=${offset}&limit=${limit}`);
        
        if (!response.ok) {
          throw new Error(`HTTP error! status: ${response.status}`);
        }
        
        const data = await response.json();
        setBlocks(data.blocks || []);
        setTotal(data.total || 0);
        setIsLoading(false);
      } catch (error) {
        console.error('Error fetching blocks:', error);
        setIsLoading(false);
      }
    };
    
    // Function to fetch a specific block
    const fetchBlock = async (id) => {
      try {
        setIsLoading(true);
        const response = await fetch(`/api/blocks/${id}`);
        
        if (!response.ok) {
          if (response.status === 404) {
            navigate('/not-found', { replace: true });
            return;
          }
          throw new Error(`HTTP error! status: ${response.status}`);
        }
        
        const data = await response.json();
        setSelectedBlock(data);
        setIsLoading(false);
      } catch (error) {
        console.error('Error fetching block:', error);
        setIsLoading(false);
      }
    };
    
    // Initial data load
    React.useEffect(() => {
      // If id parameter exists, fetch specific block
      if (params.id) {
        fetchBlock(params.id);
      } else {
        // Otherwise fetch block list
        fetchBlocks();
      }
    }, [params.id, offset, limit]);
    
    const handleBlockClick = (block) => {
      navigate(`/blocks/${block.id}`);
    };
    
    const handleBackToList = () => {
      navigate('/blocks');
    };
    
    // Detail view for a specific block
    if (params.id || selectedBlock) {
      return (
        <div className="container">
          <BlockDetail 
            block={selectedBlock} 
            onBack={handleBackToList}
          />
        </div>
      );
    }
    
    // List view of all blocks
    return (
      <div className="container">
        <div className="card">
          <div className="card-header">
            <h5 className="mb-0">All Blocks</h5>
          </div>
          <div className="card-body">
            <BlockTable 
              blocks={blocks} 
              onBlockClick={handleBlockClick}
              isLoading={isLoading}
              showPagination={true}
            />
          </div>
        </div>
      </div>
    );
  };