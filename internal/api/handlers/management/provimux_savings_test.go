package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/savings"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func provimuxSavingsGet(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/savings?"+query, nil)
	(&Handler{}).ProvimuxSavings(ctx)
	return recorder
}

func TestProvimuxSavingsAnswers200(t *testing.T) {
	recorder := provimuxSavingsGet(t, "period=today")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, property := range []string{"observed_at", "timezone", "utc_offset_seconds", "today", "range", "recording",
		"prices", "totals", "breakdowns", "models", "models_truncated", "reported_cost_hosts"} {
		if _, ok := body[property]; !ok {
			t.Errorf("missing %s", property)
		}
	}
	for _, property := range []string{"breakdowns", "models", "reported_cost_hosts"} {
		if _, ok := body[property].([]any); !ok {
			t.Errorf("%s must be [], got %v", property, body[property])
		}
	}
}

func TestProvimuxSavings400And503(t *testing.T) {
	recorder := provimuxSavingsGet(t, "period=week")
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); recorder.Code != http.StatusBadRequest || err != nil || !strings.HasPrefix(body["error"], "period:") {
		t.Fatalf("400: %d %s", recorder.Code, recorder.Body.String())
	}

	// A local store that is on and cannot be opened: 503, not 404.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = kvstore.ConfigureLocal(true, filepath.Join(blocker, "kv.db"))
	t.Cleanup(func() { _ = kvstore.ShutdownLocal() })
	recorder = provimuxSavingsGet(t, "period=today")
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"savings store unavailable"`) {
		t.Fatalf("503: %d %s", recorder.Code, recorder.Body.String())
	}
}

type provimuxSavingsWaiter chan struct{}

func (w provimuxSavingsWaiter) HandleUsage(context.Context, coreusage.Record) {
	select {
	case w <- struct{}{}:
	default:
	}
}

func TestProvimuxSavingsCarriesNoSecret(t *testing.T) {
	registry.SetLocalModelCatalogs(true)
	t.Cleanup(func() { registry.SetLocalModelCatalogs(false) })
	savings.Start()
	seen := make(provimuxSavingsWaiter, 4)
	coreusage.RegisterNamedPlugin("provimux-savings-handler-test", seen)
	coreusage.PublishRecord(context.Background(), coreusage.Record{
		Provider: "openai-compatible-openrouter", Model: "anthropic/claude-sonnet-5.5", AuthType: "apikey",
		BaseURL: "https://user:secret-pass@OpenRouter.ai:443/api/v1?token=secret-token",
		APIKey:  "sk-client-secret-key-abcd", Source: "owner-account@example.com", AuthID: "auth-id-secret-0001",
		Detail: coreusage.Detail{InputTokens: 100, OutputTokens: 10},
	})
	<-seen
	recorder := provimuxSavingsGet(t, "period=today&group=kind,provider,host,model&group=host&group=key")
	// The contract's own prices.openrouter.url is the one /api/v1 the answer may hold: it names the
	// public list, not the base URL of an entry.
	body := strings.Replace(recorder.Body.String(), `"url":"https://openrouter.ai/api/v1/models"`, "", 1)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, body)
	}
	for _, secret := range []string{"sk-client-secret-key", "secret-pass", "secret-token", "owner-account@example.com", "auth-id-secret", "/api/v1"} {
		if strings.Contains(body, secret) {
			t.Errorf("the answer carries %q", secret)
		}
	}
	if !strings.Contains(body, `"host":"openrouter.ai"`) || !strings.Contains(body, `"tail":"abcd"`) {
		t.Fatalf("host and tail: %s", body)
	}
}
