const StatCard = ({ icon, value, label, color }) => {
    return (
      <div className="col-md-3 col-sm-6">
        <div className="card stat-card">
          <div className="stat-icon" style={{ color: color || 'var(--primary-color)' }}>
            <i className={`bi bi-${icon}`}></i>
          </div>
          <div className="stat-value">{value}</div>
          <div className="stat-label">{label}</div>
        </div>
      </div>
    );
  };