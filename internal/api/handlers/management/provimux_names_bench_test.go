package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// O2 of only-my-names.md 16.7: the console's presence probe, GET /provimux/model-routes?model=, with the
// switch off (no rule is set in a benchmark process), 20 accounts of 500 names. This file uses only what the
// base (9ffc2a244) has, so the same file runs there and on the head.
func BenchmarkProvimuxO2ModelRoutesProbe(b *testing.B) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	for a := range 20 {
		auth := &coreauth.Auth{ID: fmt.Sprintf("o2-acct-%d", a), Provider: "claude", Attributes: map[string]string{"api_key": "sk"}}
		if _, err := manager.Register(b.Context(), auth); err != nil {
			b.Fatal(err)
		}
		models := make([]*registry.ModelInfo, 0, 500)
		for n := range 500 {
			models = append(models, &registry.ModelInfo{ID: fmt.Sprintf("o2-%d-%d", a, n), Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}})
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", models)
		id := auth.ID
		b.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	h := &Handler{authManager: manager}
	request := httptest.NewRequest(http.MethodGet, "/v8/management/provimux/model-routes?model=", nil)
	b.ReportAllocs()
	for b.Loop() {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = request
		h.ProvimuxModelRoutes(ctx)
	}
}
