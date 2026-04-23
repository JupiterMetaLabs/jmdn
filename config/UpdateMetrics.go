package config

// GetPoolSize returns the current number of connections in the pool
func (p *ConnectionPool) GetPoolSize() int {
	p.Mutex.RLock()
	defer p.Mutex.RUnlock()
	return len(p.Connections)
}

// GetActiveConnections returns the number of connections currently in use
func (p *ConnectionPool) GetActiveConnections() int {
	p.Mutex.RLock()
	defer p.Mutex.RUnlock()
	active := 0
	for _, conn := range p.Connections {
		if conn.InUse {
			active++
		}
	}
	return active
}

// GetIdleConnections returns the number of connections currently idle
func (p *ConnectionPool) GetIdleConnections() int {
	p.Mutex.RLock()
	defer p.Mutex.RUnlock()
	idle := 0
	for _, conn := range p.Connections {
		if !conn.InUse {
			idle++
		}
	}
	return idle
}
