package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// The console's switch writes models.live-lists through the config API. The key is a pointer now
// (unset means on), so false must reach the file and the reloaded config.
func TestProvimuxLiveListsSwitchThroughTheConfigAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\nmodels:\n  catalog: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.LiveLists != nil {
		t.Fatal("an absent key stays unset")
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	reloads := make(chan *config.Config, 4)
	h.SetConfigReloadHook(func(_ context.Context, reloaded *config.Config) { reloads <- reloaded })
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	for _, value := range []bool{false, true} {
		body := "false"
		if value {
			body = "true"
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v8/management/config/models/live-lists", strings.NewReader(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", body, recorder.Code, recorder.Body.String())
		}
		saved, _ := os.ReadFile(path)
		if !strings.Contains(string(saved), "live-lists: "+body) {
			t.Fatalf("file after PUT %s:\n%s", body, saved)
		}
		reloaded := <-reloads
		if reloaded.Models.LiveLists == nil || reloaded.Models.LiveListsOn() != value {
			t.Fatalf("reloaded after PUT %s: %v", body, reloaded.Models.LiveLists)
		}
	}
}
