const NotFound = () => {
    const navigate = ReactRouterDOM.useNavigate();
    
    return (
      <div className="container mt-5">
        <div className="row justify-content-center">
          <div className="col-md-6 text-center">
            <h1><i className="bi bi-exclamation-triangle-fill text-warning"></i></h1>
            <h2>Page Not Found</h2>
            <p className="lead">The page you are looking for doesn't exist or has been moved.</p>
            <button 
              className="btn btn-primary mt-3"
              onClick={() => navigate('/')}
            >
              <i className="bi bi-house-door me-2"></i>
              Return to Dashboard
            </button>
          </div>
        </div>
      </div>
    );
  };