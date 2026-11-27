package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
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
	v8.GET("/provimux/session-affinity-persist", s.mgmt.ProvimuxAffinityPersist)
	// The wait a stop now uses: the server's own configuration, which upstream's stop reads (a refused reload left it).
	v8.GET("/provimux/restart-drain", func(c *gin.Context) { s.mgmt.ProvimuxRestartDrainOf(c, s.getConfig()) })
	v8.GET("/provimux/same-account-retry", s.mgmt.ProvimuxSameAccountRetry)
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

// provimuxRefused is models.only-my-names for alpha search (G4 of only-my-names.md 16.3), which chooses a
// login for a named model by itself: the requested model and the model its router chose must be served, both
// judged under the one rule value loaded here. The route asks its router first (upstream's order), so the router
// sees the name before the check. Otherwise it answers 400 in the route's own error shape with the setting's
// message (counted). An empty name names no model and passes. With the rule not applying it is one atomic load.
func (s *Server) provimuxRefused(c *gin.Context, requested, selectionModel string) bool {
	rule := onlymynames.Current()
	if !rule.Applies() {
		return false
	}
	for _, name := range [2]string{strings.TrimSpace(requested), strings.TrimSpace(selectionModel)} {
		if name == "" || !rule.Refuses(name) {
			continue
		}
		onlymynames.Count(rule, name)
		c.JSON(http.StatusBadRequest, gin.H{"error": onlymynames.NotServedMessage(name)})
		return true
	}
	return false
}
