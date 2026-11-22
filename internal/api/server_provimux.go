package api

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/savings"
)

// registerProvimuxRoutes adds Provimux's own management endpoints to the authenticated v8 group,
// so they pass the group's availability, management-key and v8 middlewares like every other route.
// Stock CLIProxyAPI has none of them, so the console takes a 404 as "not a Provimux server".
func (s *Server) registerProvimuxRoutes(v8 *gin.RouterGroup) {
	savings.Start()
	v8.GET("/provimux/model-routes", s.mgmt.ProvimuxModelRoutes)
	v8.GET("/provimux/live-lists", s.mgmt.ProvimuxLiveLists)
	v8.GET("/provimux/model-lists", s.mgmt.ProvimuxModelLists)
	v8.GET("/provimux/savings", s.mgmt.ProvimuxSavings)
}
