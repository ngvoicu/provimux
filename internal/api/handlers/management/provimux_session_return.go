package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux-only: GET /v8/management/provimux/session-return, the state of the rule of session-route
// (.github/provimux/design/session-route.md 15.4; contract: session-route/openapi.yaml, SessionReturnState): the
// setting, whether the rule is in effect and, if not, why, the binding lifetime in use, and the exact count of
// conversations the rule returned since the start. Read-only, from memory. There is no list of
// bindings and no release here.
func (h *Handler) ProvimuxSessionReturn(c *gin.Context) {
	c.JSON(http.StatusOK, coreauth.ProvimuxSessionReturnStateOf(h.provimuxAuthManager(), provimuxNow()))
}
