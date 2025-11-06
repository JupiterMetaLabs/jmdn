package explorer

import (
	"gossipnode/DB_OPs"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func (s *ImmuDBServer) listDIDs(c *gin.Context) {

	// Get network parameter from URL path (optional)
	network := c.Query("network")

	// Get pagination parameters with defaults
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

	// Validate pagination parameters
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 100 // Default to 10 items per page, max 100
	}

	// Calculate offset for pagination
	offset := (page - 1) * limit

	// Get the paginated list of DIDs from the database.
	// This is now efficient as it only fetches the documents for the current page.
	pagedDIDs, err := DB_OPs.ListAccountsPaginated(&s.accountsdb, limit, offset, network)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve DIDs: " + err.Error()})
		return
	}

	// Calculate total pages for pagination metadata
	totalPages := (len(pagedDIDs) + limit - 1) / limit

	c.JSON(http.StatusOK, gin.H{
		"data":       pagedDIDs,
		"pagination": getPaginationMetadata(page, limit, totalPages),
	})
}

// Helper function to create pagination metadata
func getPaginationMetadata(page, limit, totalPages int) gin.H {
	hasNext := page < totalPages
	hasPrev := page > 1

	return gin.H{
		"current_page": page,
		"per_page":     limit,
		"total_pages":  totalPages,
		"has_next":     hasNext,
		"has_prev":     hasPrev,
	}
}

func (s *ImmuDBServer) getDIDDetails(c *gin.Context) {
	// list of DIDs in the request
	DIDs := c.QueryArray("dids")
	if len(DIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one 'dids' query parameter is required"})
		return
	}

	var didDocs []DB_OPs.Account
	for _, DID := range DIDs {
		DID_Doc, err := DB_OPs.GetAccountByDID(&s.accountsdb, DID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		didDocs = append(didDocs, *DID_Doc)
	}
	c.JSON(http.StatusOK, didDocs)
}
