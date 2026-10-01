package rpc

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"gossipnode/config/settings"
	"gossipnode/internal/syncmonitor"
	"gossipnode/pkg/gatekeeper"
)

// RegisterSyncRoutes adds the /sync/* endpoints to an existing gin router.
// Call this before serving, passing the running SyncMonitor.
//
//	GET  /sync/status    — returns the cached last sync check result
//	POST /sync/reconcile — triggers an immediate check + reconcile (admin auth required)
func RegisterSyncRoutes(router gin.IRouter, monitor *syncmonitor.Monitor) {
	grp := router.Group("/sync")
	grp.GET("/status", makeSyncStatusHandler(monitor))

	// JMDN-H05: reconcile runs a synchronous Merkle build + seednode report —
	// require admin_http token (ADMIN_TOKEN / JWT). Unauthenticated → 401/403.
	var secCfg *settings.SecurityConfig
	if settings.IsLoaded() {
		cfg := settings.Get().Security
		secCfg = &cfg
	}
	grp.POST("/reconcile", gatekeeper.AdminHTTPMiddleware(secCfg), makeSyncReconcileHandler(monitor))
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
