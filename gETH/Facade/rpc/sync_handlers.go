package rpc

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"gossipnode/internal/syncmonitor"
)

// RegisterSyncRoutes adds the /sync/* endpoints to an existing gin router.
// Call this before serving, passing the running SyncMonitor.
//
//	GET  /sync/status    — returns the cached last sync check result
//	POST /sync/reconcile — triggers an immediate check + reconcile and returns the result
func RegisterSyncRoutes(router gin.IRouter, monitor *syncmonitor.Monitor) {
	grp := router.Group("/sync")
	grp.GET("/status", makeSyncStatusHandler(monitor))
	grp.POST("/reconcile", makeSyncReconcileHandler(monitor))
}

func makeSyncStatusHandler(monitor *syncmonitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		st := monitor.GetStatus()
		code := http.StatusOK
		if st.Error != "" {
			code = http.StatusInternalServerError
		}
		c.JSON(code, st)
	}
}

func makeSyncReconcileHandler(monitor *syncmonitor.Monitor) gin.HandlerFunc {
	return func(c *gin.Context) {
		// TriggerCheck is synchronous for the Merkle build + seednode report;
		// the actual block/account reconcile runs in a background goroutine.
		st := monitor.TriggerCheck(c.Request.Context())
		code := http.StatusOK
		if st.Error != "" {
			code = http.StatusInternalServerError
		}
		c.JSON(code, st)
	}
}
