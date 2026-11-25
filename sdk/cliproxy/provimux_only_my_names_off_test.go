package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// O1 (only-my-names.md 16.7): with models.only-my-names absent, and with enabled: false and a filled keep,
// the three list shapes, a chat by alias, by a vendor's name and by an unknown name, and every client's
// registered models equal a golden file produced by this same file at 9ffc2a244, the commit before the
// build. The file uses upstream's API only, so it runs there unchanged: PROVIMUX_O1_WRITE=<path> writes the
// golden file instead of comparing. A local stand-in answers every request; nothing leaves the machine.

const namesOffGolden = "testdata/provimux_only_my_names_off.golden.json"

const namesOffConfig = `access:
  api-keys:
    - o1-client-key
api-keys:
  openai-compatibility:
    - name: standin
      base-url: VENDOR/v1
      models:
        - name: up-mine
          alias: mine
        - name: held-compat
      keys:
        - api-key: sk-o1
    - name: teamed
      base-url: VENDOR/t/v1
      prefix: team-a
      models:
        - name: up-t
          alias: mine-t
      keys:
        - api-key: sk-o1-t
  claude:
    - name: claude-a
      base-url: VENDOR
      models:
        - name: claude-up
          alias: claude-mine
        - name: held-claude
      keys:
        - api-key: sk-o1-claude
`

type namesOffObservation struct {
	Lists      map[string][]string `json:"lists"`
	Chats      map[string]string   `json:"chats"`
	Registered map[string][]string `json:"registered"`
}

func namesOffRun(t *testing.T, setting string) namesOffObservation {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+payload.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"c","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, payload.Model)
	}))
	defer vendor.Close()
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	raw := fmt.Sprintf("server:\n  port: 54322\noauth:\n  auth-dir: %q\n%s%sconfig-version: 8\n", filepath.ToSlash(authDir), strings.ReplaceAll(namesOffConfig, "VENDOR", vendor.URL), setting)
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := internalconfig.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(configPath).Build()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.registerConfigAPIKeyAuths(coreauth.WithSkipPersist(ctx), service.currentConfig())
	defer func() {
		for _, auth := range service.coreManager.List() {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
	}()
	server := api.NewServer(service.currentConfig(), service.coreManager, service.accessManager, configPath, service.serverOptions...)
	front := httptest.NewServer(server.Handler())
	defer front.Close()
	deadline := time.Now().Add(10 * time.Second)
	for _, name := range []string{"mine", "held-compat", "team-a/mine-t", "claude-mine", "held-claude"} {
		for registry.GetGlobalRegistry().GetModelCount(name) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("%s is not registered", name)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	do := func(method, path string, header http.Header, body string) (int, string) {
		request, errRequest := http.NewRequest(method, front.URL+path, strings.NewReader(body))
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		request.Header.Set("Authorization", "Bearer o1-client-key")
		request.Header.Set("Content-Type", "application/json")
		for name, values := range header {
			request.Header[name] = values
		}
		response, errDo := front.Client().Do(request)
		if errDo != nil {
			t.Fatal(errDo)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	ids := func(body string) []string {
		var list struct {
			Data   []map[string]any `json:"data"`
			Models []map[string]any `json:"models"`
		}
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			t.Fatalf("decode %s", body)
		}
		var out []string
		for _, entry := range list.Data {
			out = append(out, fmt.Sprint(entry["id"]))
		}
		for _, entry := range list.Models {
			out = append(out, fmt.Sprint(entry["name"]))
		}
		sort.Strings(out)
		return out
	}
	observation := namesOffObservation{Lists: map[string][]string{}, Chats: map[string]string{}, Registered: map[string][]string{}}
	_, body := do("GET", "/v1/models", nil, "")
	observation.Lists["openai"] = ids(body)
	_, body = do("GET", "/v1/models", http.Header{"Anthropic-Version": {"2023-06-01"}}, "")
	observation.Lists["claude"] = ids(body)
	_, body = do("GET", "/v1beta/models", nil, "")
	observation.Lists["gemini"] = ids(body)
	for _, model := range []string{"mine", "held-compat", "nobody-offers-this"} {
		mu.Lock()
		before := len(seen)
		mu.Unlock()
		status, answer := do("POST", "/v1/chat/completions", nil, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model))
		mu.Lock()
		reached := strings.Join(seen[before:], ",")
		mu.Unlock()
		if status != http.StatusOK {
			observation.Chats[model] = fmt.Sprintf("%d %s | vendor: %s", status, strings.TrimSpace(answer), reached)
		} else {
			observation.Chats[model] = fmt.Sprintf("%d | vendor: %s", status, reached)
		}
	}
	for _, auth := range service.coreManager.List() {
		label := auth.Provider
		if name := auth.Attributes["compat_name"]; name != "" {
			label += "/" + name
		}
		var models []string
		for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
			encoded, errEncode := json.Marshal(model)
			if errEncode != nil {
				t.Fatal(errEncode)
			}
			var fields map[string]any
			_ = json.Unmarshal(encoded, &fields)
			delete(fields, "created") // set from the clock for configured models
			normalized, _ := json.Marshal(fields)
			models = append(models, string(normalized))
		}
		sort.Strings(models)
		observation.Registered[label] = models
	}
	return observation
}

func TestProvimuxOnlyMyNamesOffIsToday(t *testing.T) {
	var first *namesOffObservation
	for _, variant := range []struct{ name, setting string }{
		{"absent", ""},
		{"enabled false, keep filled", "models:\n  only-my-names:\n    enabled: false\n    keep:\n      - held-compat\n      - claude-mine\n"},
	} {
		t.Run(variant.name, func(t *testing.T) {
			got := namesOffRun(t, variant.setting)
			if path := os.Getenv("PROVIMUX_O1_WRITE"); path != "" {
				if first == nil {
					first = &got
					data, _ := json.MarshalIndent(got, "", "  ")
					if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
						t.Fatal(err)
					}
				} else if !reflect.DeepEqual(got, *first) {
					t.Fatalf("writing the golden file: %s differs from the first variant", variant.name)
				}
				return
			}
			data, err := os.ReadFile(namesOffGolden)
			if err != nil {
				t.Fatal(err)
			}
			var want namesOffObservation
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("differs from the base's golden file %s:\n%s", namesOffGolden, gotJSON)
			}
		})
	}
}
