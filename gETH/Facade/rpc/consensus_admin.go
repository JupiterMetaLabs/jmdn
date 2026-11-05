package rpc

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/JupiterMetaLabs/ion"
	"github.com/gin-gonic/gin"

	"gossipnode/config/settings"
	"gossipnode/internal/roundlock"
	"gossipnode/pkg/gatekeeper"
)

// RegisterConsensusAdminRoutes adds the admin-only consensus recovery
// endpoints:
//
//	POST /consensus/round-ledger/clear {"height": N, "include_signing_ledger": false}
//
// clears this node's vote ledger (roundlock.Votes) for one height above the
// committed tip, so a height whose retries the ledger refuses can commit
// again. The result-signing ledger (roundlock.Default) is cleared only with
// include_signing_ledger: true. That ledger is what stops this node signing
// two blocks in one round; clear it only when the height is known to have no
// certificate anywhere, including one a sequencer may be withholding. Every
// cleared round is logged with its block identity. With no config loaded
// nothing is authorized.
func RegisterConsensusAdminRoutes(router gin.IRouter) {
	var secCfg *settings.SecurityConfig
	if settings.IsLoaded() {
		secCfg = &settings.Get().Security
	}
	router.POST("/consensus/round-ledger/clear", gatekeeper.AdminHTTPMiddleware(secCfg), handleClearRoundLedger)
}

type clearRoundLedgerRequest struct {
	Height               *uint64 `json:"height"`
	IncludeSigningLedger bool    `json:"include_signing_ledger"`
}

// describeCleared renders period -> block identity in period order.
func describeCleared(m map[uint64]string) string {
	periods := make([]uint64, 0, len(m))
	for p := range m {
		periods = append(periods, p)
	}
	sort.Slice(periods, func(i, j int) bool { return periods[i] < periods[j] })
	parts := make([]string, 0, len(periods))
	for _, p := range periods {
		parts = append(parts, fmt.Sprintf("%d=%s", p, m[p]))
	}
	return strings.Join(parts, ",")
}

func handleClearRoundLedger(c *gin.Context) {
	var req clearRoundLedgerRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Height == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": `body must be {"height": <block number>}`})
		return
	}
	h := *req.Height
	votes, err := roundlock.Votes.ClearHeight(h)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	var signer map[uint64]string
	if req.IncludeSigningLedger {
		if signer, err = roundlock.Default.ClearHeight(h); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "votes_cleared": describeCleared(votes)})
			return
		}
	}
	if l := logger(); l != nil {
		l.Warn(c.Request.Context(), "Operator cleared round-ledger records for a height",
			ion.Uint64("height", h),
			ion.String("votes_cleared", describeCleared(votes)),
			ion.Bool("signing_ledger_included", req.IncludeSigningLedger),
			ion.String("signer_cleared", describeCleared(signer)))
	}
	c.JSON(http.StatusOK, gin.H{
		"height":                  h,
		"votes_cleared":           describeCleared(votes),
		"signing_ledger_included": req.IncludeSigningLedger,
		"signer_cleared":          describeCleared(signer),
	})
}
