package provimux

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: finding 2 of the review of the rule's first build, the reviewer's own HTTP case, against the rule as built
// now (session-route.md 15). The image route may not use a free account. Two paid Codex accounts of priority 10 and a
// free one of priority 20 serve the image model; a conversation named by Session-Id is bound to a paid account, its
// first route for that route. Unused for cold-after, its next request goes to the same paid account, with the rule set
// and without it (the first build moved it to the other paid one). Every key and account is invented; the executor
// is a stand-in in this process whose answer names the account that answered.

type sessionReturnImageExecutor struct{ coreauth.ProviderExecutor }

func (sessionReturnImageExecutor) Identifier() string { return "codex" }

func (sessionReturnImageExecutor) Execute(ctx context.Context, a *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"created":1,"data":[{"revised_prompt":%q,"b64_json":"aW1n"}]}`, a.ID))}, nil
}

func TestProvimuxSessionReturnImageRoute(t *testing.T) {
	const cold, ttl = 2 * time.Hour, 4 * time.Hour
	for _, row := range []struct {
		name     string
		set      bool
		topPlan  string // the plan of the priority-20 account
		returned bool
	}{
		{name: "rule unset", topPlan: "free"},
		{name: "rule set", set: true, topPlan: "free"},
		// The control: the top account is paid, so the image route may use it. It cooled when the conversation began,
		// is free again, and the conversation returns to it.
		{name: "rule set, the top account paid (control)", set: true, topPlan: "plus", returned: true},
	} {
		set := row.set
		t.Run(row.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			affinity := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.RoundRobinSelector{}, TTL: ttl})
			manager.SetSelector(affinity)
			t.Cleanup(affinity.Stop)
			manager.RegisterExecutor(sessionReturnImageExecutor{})
			for _, v := range []struct{ id, priority, plan string }{{"a", "10", "plus"}, {"b", "10", "plus"}, {"free", "20", row.topPlan}} {
				id := "session-return-image-" + v.id
				if _, err := manager.Register(coreauth.WithSkipPersist(t.Context()), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive,
					Attributes: map[string]string{"priority": v.priority, "plan_type": v.plan, "api_key": "synthetic"}}); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-image-1.5", Object: "model"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			cfg := &config.Config{}
			cfg.APIKeys = []string{"session-return-image-key"}
			cfg.AuthDir = t.TempDir()
			cfg.Routing.SessionAffinity = true
			server := api.NewServer(cfg, manager, sdkaccess.NewManager(), filepath.Join(t.TempDir(), "config.yaml"))
			if set {
				coreauth.ProvimuxSetSessionReturn(manager, cold)
				t.Cleanup(func() { coreauth.ProvimuxSetSessionReturn(nil, 0) })
			}
			turn := func() string {
				t.Helper()
				req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-1.5","prompt":"synthetic"}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Session-Id", "never-fallback")
				req.Header.Set("Authorization", "Bearer session-return-image-key")
				recorder := httptest.NewRecorder()
				server.Handler().ServeHTTP(recorder, req)
				if recorder.Code != http.StatusOK {
					t.Fatalf("HTTP %d %s", recorder.Code, recorder.Body.String())
				}
				return recorder.Body.String()
			}
			top := "session-return-image-free"
			if row.returned {
				hour := time.Hour
				manager.MarkResult(context.Background(), coreauth.Result{AuthID: top, Provider: "codex", Model: "gpt-image-1.5",
					Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic"}, RetryAfter: &hour})
			}
			first := turn()
			if !strings.Contains(first, "session-return-image-a") && !strings.Contains(first, "session-return-image-b") {
				t.Fatalf("first answer from %s", first)
			}
			if row.returned {
				manager.MarkResult(context.Background(), coreauth.Result{AuthID: top, Provider: "codex", Model: "gpt-image-1.5", Success: true})
			}
			before := coreauth.ProvimuxSessionReturnStateOf(manager, time.Now()).Counts.Returned
			coreauth.ProvimuxSessionReturnAgeForTest(manager, cold+time.Hour)
			next := turn()
			after := coreauth.ProvimuxSessionReturnStateOf(manager, time.Now()).Counts.Returned
			if row.returned {
				if !strings.Contains(next, top) || after-before != 1 {
					t.Fatalf("control: the next answer is %s, counted %d; want the top account, once", next, after-before)
				}
				return
			}
			if next != first || after != before {
				t.Fatalf("unused for cold-after, the next answer is %s, counted %d; want the same paid account as %s", next, after-before, first)
			}
		})
	}
}
