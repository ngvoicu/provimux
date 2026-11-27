package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Provimux: GET /v8/management/provimux/same-account-retry says the value in use and whether the file sets it; the
// console writes routing.retry.same-account-retry through the generic config path, and the state follows each write;
// a value is taken as upstream takes one for its own switches.
// Inside a test binary the default is off (config.ProvimuxSameAccountRetryProcessDefault); the gateway's is on.
func TestProvimuxSameAccountRetryStateAndGenericPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if provimuxOpenAPISchemas(t)["SameAccountRetryResponse"] == nil {
		t.Fatal("the contract has no SameAccountRetryResponse")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\nserver:\n  port: 8317\nrouting:\n  retry:\n    request-retry: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	reloads := make(chan *config.Config, 16)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { reloads <- cfg })
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}
	router.GET("/v8/management/provimux/same-account-retry", h.ProvimuxSameAccountRetry)
	request := func(method, url, body string, status int) string {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
		if recorder.Code != status {
			t.Fatalf("%s %s %s: %d %s, want %d", method, url, body, recorder.Code, recorder.Body.String(), status)
		}
		return recorder.Body.String()
	}
	state := func(enabled, set bool) {
		t.Helper()
		body := request(http.MethodGet, "/v8/management/provimux/same-account-retry", "", http.StatusOK)
		t.Logf("state: %s", body)
		provimuxValidate(t, "SameAccountRetryResponse", []byte(body))
		if got := ojson.Get(body, "enabled").Bool(); got != enabled {
			t.Fatalf("enabled %v, want %v: %s", got, enabled, body)
		}
		if got := ojson.Get(body, "set_in_file").Bool(); got != set {
			t.Fatalf("set_in_file %v, want %v: %s", got, set, body)
		}
		if ojson.Get(body, "default").Bool() != config.ProvimuxSameAccountRetryProcessDefault() || ojson.Get(body, "applies").Bool() != enabled ||
			ojson.Get(body, "wait_min_ms").Int() != 500 || ojson.Get(body, "wait_max_ms").Int() != 1000 || ojson.Get(body, "retry_after_max_ms").Int() != 2000 ||
			!ojson.Get(body, "second_tries_sent").Exists() || !ojson.Get(body, "second_tries_answered").Exists() || !ojson.Get(body, "second_tries_failed").Exists() || !ojson.Get(body, "left_during_wait").Exists() || !ojson.Get(body, "since").Exists() {
			t.Fatalf("state: %s", body)
		}
	}
	waitValue := func(on, set bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case cfg := <-reloads:
				if gotOn, gotSet := cfg.ProvimuxSameAccountRetry(); gotOn == on && gotSet == set {
					return
				}
			case <-deadline:
				t.Fatalf("no reload with (%v, %v)", on, set)
			}
		}
	}
	const key = "/v8/management/config/routing/retry/same-account-retry"

	state(config.ProvimuxSameAccountRetryProcessDefault(), false)
	request(http.MethodGet, key, "", http.StatusNotFound)
	request(http.MethodPut, key, "false", http.StatusOK)
	waitValue(false, true)
	state(false, true)
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "same-account-retry: false") {
		t.Fatalf("file:\n%s", data)
	}
	request(http.MethodPut, key, "true", http.StatusOK)
	waitValue(true, true)
	state(true, true)
	request(http.MethodGet, key, "", http.StatusOK)
	// A string is taken as upstream takes one for its own switches (disable-cooling "yes" loads as true).
	request(http.MethodPut, key, `"yes"`, http.StatusOK)
	if loaded, errLoad := config.LoadConfig(path); errLoad != nil {
		t.Fatal(errLoad)
	} else if on, set := loaded.ProvimuxSameAccountRetry(); !on || !set {
		t.Fatalf(`"yes": (%v, %v), want (true, true) as upstream's switches`, on, set)
	}
	request(http.MethodDelete, key, "", http.StatusOK)
	waitValue(config.ProvimuxSameAccountRetryProcessDefault(), false)
	state(config.ProvimuxSameAccountRetryProcessDefault(), false)
}

// The key on its own: unset is the process default; true and false are read from the file.
func TestProvimuxSameAccountRetryConfig(t *testing.T) {
	for text, want := range map[string][2]bool{
		"":                                {config.ProvimuxSameAccountRetryProcessDefault(), false},
		"    same-account-retry: true\n":  {true, true},
		"    same-account-retry: false\n": {false, true},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("config-version: 8\nrouting:\n  retry:\n    request-retry: 3\n"+text), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if on, set := cfg.ProvimuxSameAccountRetry(); on != want[0] || set != want[1] {
			t.Errorf("%q: (%v, %v), want %v", text, on, set, want)
		}
	}
	if on, set := (*config.Config)(nil).ProvimuxSameAccountRetry(); on != config.ProvimuxSameAccountRetryProcessDefault() || set {
		t.Errorf("nil config: (%v, %v)", on, set)
	}
}
