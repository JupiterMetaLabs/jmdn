package explorer

import (
	"gossipnode/DB_OPs"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func (s *ImmuDBServer) listDIDs(c *gin.Context) {
    // Get pagination parameters with defaults
    page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
    limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

    // Validate pagination parameters
    if page < 1 {
        page = 1
    }
    if limit < 1 || limit > 50 {
        limit = 10 // Default to 10 items per page, max 50
    }

    // Get all DIDs (consider optimizing this in DB_OPs if needed)
    allDIDs, err := DB_OPs.ListAllDIDs(&s.accountsdb, 1000) // Using a large limit to get all
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    totalDIDs := len(allDIDs)

    // Calculate pagination
    totalPages := (totalDIDs + limit - 1) / limit
    if page > totalPages && totalPages > 0 {
        page = totalPages
    }

    // Calculate start and end indices
    start := (page - 1) * limit
    if start >= totalDIDs {
        c.JSON(http.StatusOK, gin.H{
            "data":       []string{},
            "pagination": getPaginationMetadata(page, limit, totalDIDs, totalPages),
        })
        return
    }

    end := start + limit
    if end > totalDIDs {
        end = totalDIDs
    }

    // Get paginated results
    pagedDIDs := allDIDs[start:end]

    c.JSON(http.StatusOK, gin.H{
        "data":       pagedDIDs,
        "pagination": getPaginationMetadata(page, limit, totalDIDs, totalPages),
    })
}

// Helper function to create pagination metadata
func getPaginationMetadata(page, limit, totalItems, totalPages int) gin.H {
    hasNext := page < totalPages
    hasPrev := page > 1

    return gin.H{
        "current_page": page,
        "per_page":     limit,
        "total_pages":  totalPages,
        "total_items":  totalItems,
        "has_next":     hasNext,
        "has_prev":     hasPrev,
    }
}


func (s *ImmuDBServer) getDIDDetails(c *gin.Context) {
	// list of DIDs in the request
	DIDs := c.QueryArray("dids")
	
	var didDocs []DB_OPs.DIDDocument
	for _,DID := range DIDs {
		DID_Doc, err := DB_OPs.GetDID(&s.accountsdb, DID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		didDocs = append(didDocs, *DID_Doc)
	}
	c.JSON(http.StatusOK, didDocs)
}
