const Navbar = () => {
    const { Link } = ReactRouterDOM;
    const location = ReactRouterDOM.useLocation();
    
    return (
      <nav className="navbar navbar-expand-lg navbar-dark mb-4">
        <div className="container">
          <Link className="navbar-brand" to="/">
            <i className="bi bi-boxes me-2"></i>
            Blockchain Explorer
          </Link>
          <button className="navbar-toggler" type="button" data-bs-toggle="collapse" data-bs-target="#navbarNav">
            <span className="navbar-toggler-icon"></span>
          </button>
          <div className="collapse navbar-collapse" id="navbarNav">
            <ul className="navbar-nav ms-auto">
              <li className="nav-item">
                <Link className={`nav-link ${location.pathname === '/' ? 'active' : ''}`} to="/">
                  <i className="bi bi-speedometer2 me-1"></i> Dashboard
                </Link>
              </li>
              <li className="nav-item">
                <Link className={`nav-link ${location.pathname === '/transactions' ? 'active' : ''}`} to="/transactions">
                  <i className="bi bi-arrow-left-right me-1"></i> Transactions
                </Link>
              </li>
              <li className="nav-item">
                <Link className={`nav-link ${location.pathname === '/blocks' ? 'active' : ''}`} to="/blocks">
                  <i className="bi bi-box me-1"></i> Blocks
                </Link>
              </li>
            </ul>
          </div>
        </div>
      </nav>
    );
  };