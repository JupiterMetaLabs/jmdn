package rpc

import (
	"net/http"

	"github.com/JupiterMetaLabs/ion"
	"github.com/gin-gonic/gin"

	"gossipnode/config/settings"
	"gossipnode/internal/roundlock"
	"gossipnode/pkg/gatekeeper"
)

// RegisterConsensusAdminRoutes adds the admin-only consensus recovery
// endpoints:
//
//	POST /consensus/round-ledger/clear {"height": N}
//
// clears this node's vote and signing ledgers for one height above the
// committed tip (roundlock.Ledger.ClearHeight). Use it only on a stuck height
// that has no certificate anywhere in the network, on enough nodes to reach
// quorum again. With no config loaded nothing is authorized.
func RegisterConsensusAdminRoutes(router gin.IRouter) {
	var secCfg *settings.SecurityConfig
	if settings.IsLoaded() {
		secCfg = &settings.Get().Security
	}
	router.POST("/consensus/round-ledger/clear", gatekeeper.AdminHTTPMiddleware(secCfg), handleClearRoundLedger)
}

type clearRoundLedgerRequest struct {
	Height *uint64 `json:"height"`
}

func handleClearRoundLedger(c *gin.Context) {
	var req clearRoundLedgerRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Height == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": `body must be {"height": <block number>}`})
		return
	}
	h := *req.Height
	votes, verr := roundlock.Votes.ClearHeight(h)
	signer, serr := roundlock.Default.ClearHeight(h)
	if verr != nil || serr != nil {
		msg := ""
		if verr != nil {
			msg = verr.Error()
		} else {
			msg = serr.Error()
		}
		c.JSON(http.StatusConflict, gin.H{"error": msg, "votes_cleared": votes, "signer_cleared": signer})
		return
	}
	if l := logger(); l != nil {
		l.Warn(c.Request.Context(), "Operator cleared round-ledger records for a height",
			ion.Uint64("height", h), ion.Bool("votes_cleared", votes), ion.Bool("signer_cleared", signer))
	}
	c.JSON(http.StatusOK, gin.H{"height": h, "votes_cleared": votes, "signer_cleared": signer})
}
