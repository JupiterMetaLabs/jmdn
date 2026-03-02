package explorer

import (
	"net/http"
	"time"

	"jmdn/DB_OPs"
	"jmdn/config"

	"github.com/gin-gonic/gin"
)

type health struct {
	status     string
	statusCode int
	service    string
	database   string
	timestamp  time.Time
}

// healthCheck handles health check requests for the default database
func (s *ImmuDBServer) healthCheck(c *gin.Context) {
	value, err := s.checkHealth(c, s.defaultdb.Client, "defaultdb")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, value)
}

// didHealthCheck handles health check requests for the accounts database
func (s *ImmuDBServer) didHealthCheck(c *gin.Context) {
	health, err := s.checkHealth(c, s.accountsdb.Client, "AccountsDB")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, health)
}

// checkHealth is a helper function that performs the actual health check
func (s *ImmuDBServer) checkHealth(c *gin.Context, db *config.ImmuClient, dbType string) (*health, error) {
	isHealthy := DB_OPs.IsHealthy(db)

	var status string
	var statusCode int

	if isHealthy {
		status = "ok"
		statusCode = http.StatusOK
	} else {
		status = "error"
		statusCode = http.StatusServiceUnavailable
	}

	return &health{
		status:     status,
		statusCode: statusCode,
		service:    "immudb",
		database:   dbType,
		timestamp:  time.Now().UTC(),
	}, nil
}
