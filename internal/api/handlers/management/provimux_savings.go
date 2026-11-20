package management

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/savings"
)

// ProvimuxSavings handles GET /v8/management/provimux/savings: recorded daily usage totals of a range,
// valued at list prices (.github/provimux/design/savings/03-endpoint.md). Read-only: it writes
// nothing and starts no download. All logic is in internal/provimux/savings.
func (h *Handler) ProvimuxSavings(c *gin.Context) {
	request, errRequest := savings.ParseRequest(c.Request.URL.Query())
	if errRequest != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errRequest.Error()})
		return
	}
	answer, errQuery := savings.Query(c.Request.Context(), request)
	if errQuery != nil {
		if errors.Is(errQuery, savings.ErrStoreUnavailable) {
			// Not 404: the console must not take this for an absent endpoint.
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "savings store unavailable"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "savings unavailable"})
		return
	}
	c.JSON(http.StatusOK, answer)
}
