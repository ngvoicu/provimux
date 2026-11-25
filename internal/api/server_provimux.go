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
	v8.GET("/provimux/names", s.mgmt.ProvimuxNames)
}

// ProvimuxRoutes lists the method and path of every route the server registered, for Provimux's tests
// that must notice a route upstream adds (only-my-names test D2).
func (s *Server) ProvimuxRoutes() []string {
	if s == nil || s.engine == nil {
		return nil
	}
	routes := s.engine.Routes()
	out := make([]string, 0, len(routes))
	for _, route := range routes {
		out = append(out, route.Method+" "+route.Path)
	}
	return out
}
