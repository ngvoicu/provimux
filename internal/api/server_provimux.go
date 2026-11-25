package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/savings"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
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

// provimuxRefused is models.only-my-names for alpha search (G4 of only-my-names.md 16.3), which chooses a
// login for a named model by itself: the requested model and the model its router chose must be served. The
// route asks its router first (upstream's order), so the router sees the name before the check.
// Otherwise it answers 400 in the route's own error shape, with the setting's message when an account
// registers the name (the refusal is then counted) and upstream's unknown-model words when none does. An
// empty name names no model and passes. With the rule not applying it is one atomic load.
func (s *Server) provimuxRefused(c *gin.Context, requested, selectionModel string) bool {
	if !registry.ProvimuxApplies() {
		return false
	}
	requested, selectionModel = strings.TrimSpace(requested), strings.TrimSpace(selectionModel)
	for i, name := range [2]string{requested, selectionModel} {
		if name == "" || (i == 1 && name == requested) {
			continue
		}
		refused, offered := registry.ProvimuxRefuse(name)
		if !refused {
			continue
		}
		message := "unknown provider for model " + name
		if offered {
			message = registry.ProvimuxNotServedMessage(registry.ProvimuxBase(name))
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": message})
		return true
	}
	return false
}
