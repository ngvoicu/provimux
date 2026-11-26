package cliproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// A compatibility key's own settings follow its endpoint across a reload (key-model-lists.md 6.1).
// Another endpoint is inserted ahead of the key's own: until the watcher hands out the refreshed
// credential, the key still carries its old config index, which now names that other endpoint.
// The key must keep its own excluded-models and fixed-models in its registration, in the account
// list, in its status row and in the live-list refresh, the way upstream finds its endpoint again
// (provider name and base URL), never by the stale position.
func TestProvimuxKeyRulesFollowEndpointAcrossReload(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, path := range []string{"reload", "registration", "registration-cache"} {
			name := path + "/distinct-names"
			if shared {
				name = path + "/shared-name"
			}
			t.Run(name, func(t *testing.T) {
				provimuxLiveReset(t)
				t.Setenv("WRITABLE_PATH", t.TempDir())
				live := keyServe([]byte(`{"data":[{"id":"shown"},{"id":"withheld"},{"id":"live-only"}]}`))
				targetStand, otherStand := newKeyStandIn(t, live), newKeyStandIn(t, live)
				// The account list names the host only: the key's endpoint is "localhost", the other one
				// "127.0.0.1" (both the same loopback, both httptest stand-ins).
				_, port, _ := net.SplitHostPort(targetStand.Listener.Addr().String())
				targetBase := "http://localhost:" + port

				old := keyCompatConfig(targetBase+"/v1",
					config.OpenAICompatibilityModel{Name: "shown"}, config.OpenAICompatibilityModel{Name: "withheld"})
				old.AuthDir = t.TempDir()
				target := old.OpenAICompatibility[0]
				target.Name, target.FixedModels, target.ExcludedModels = "target", true, []string{"withheld"}
				old.OpenAICompatibility[0] = target

				other := keyCompatConfig(otherStand.URL+"/v1", config.OpenAICompatibilityModel{Name: "other-model"}).OpenAICompatibility[0]
				other.Name, other.ExcludedModels = "other", []string{"shown"}
				if shared {
					other.Name = "target"
				}
				reordered := *old
				reordered.OpenAICompatibility = []config.OpenAICompatibility{other, target}

				svc, keys := keyService(t, old)
				svc.pluginHost = pluginhost.New()
				key := keys[0]
				svc.registerModelsForAuth(context.Background(), key)
				if got := provimuxRegistered(key); !slices.Equal(got, []string{"shown"}) {
					t.Fatalf("before the reload: registered %v, want [shown]", got)
				}

				switch path {
				case "reload": // the watcher's service callback, called before it publishes refreshed credentials
					svc.applyWatcherConfigUpdate(&reordered)
				case "registration":
					svc.cfg = &reordered
					svc.registerModelsForAuth(context.Background(), key)
				case "registration-cache":
					svc.cfg = &reordered
					svc.registerModelsForAuthWithCache(context.Background(), key, svc.newOpenAICompatibilityRegistrationCache())
				}
				cfg := svc.currentConfig()
				if key.Attributes[coreauth.AttributeConfigIndex] != "0" {
					t.Fatalf("fixture: the key's config index is %q, want the stale 0", key.Attributes[coreauth.AttributeConfigIndex])
				}

				if got := provimuxRegistered(key); !slices.Equal(got, []string{"shown"}) {
					t.Errorf("after the reload: registered %v, want [shown] (its own exclusions)", got)
				}
				if kind, notAsked := keyModelListKind(cfg, key); kind != "" || notAsked != keyNotAskedFixed {
					t.Errorf("list kind %q, not asked %q; want not asked: fixed", kind, notAsked)
				}
				if kind := liveModelListKindFor(cfg, key); kind != "" {
					t.Errorf("live-list kind %q; a fixed key is never fetched", kind)
				}
				if _, row, ok := (provimuxLiveListSource{}).KeyModelList(cfg, key, true, false, false); !ok || !row.Fixed || row.NotAsked != keyNotAskedFixed || row.Configured != 2 {
					t.Errorf("status row ok=%v %+v; want fixed, not asked: fixed, 2 configured", ok, row)
				}

				// The account list a client reads.
				router := gin.New()
				router.GET("/v1/models", openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, svc.coreManager)).OpenAIModels)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models?include=accounts", nil))
				var offered []string
				for _, model := range ojson.GetBytes(rec.Body.Bytes(), "data").Array() {
					for _, account := range model.Get("accounts").Array() {
						if account.Get("host").String() == "localhost" {
							offered = append(offered, model.Get("id").String())
						}
					}
				}
				slices.Sort(offered)
				if !slices.Equal(offered, []string{"shown"}) {
					t.Errorf("GET /v1/models?include=accounts offers %v on the key's account, want [shown]", offered)
				}

				// A forced refresh of the live lists leaves the fixed key alone.
				keyRefresh(svc, true)
				if n := len(targetStand.seen()) + len(otherStand.seen()); n != 0 {
					t.Errorf("the fixed key's list was asked for %d time(s)", n)
				}
				if got := provimuxRegistered(key); !slices.Equal(got, []string{"shown"}) {
					t.Errorf("after a refresh: registered %v, want [shown]", got)
				}
			})
		}
	}
}

// keyCompatEntry is upstream's endpoint match: in every case the entry it names is the one whose
// models upstream's registration registered for the key, cached and uncached (no exclusions and
// no fixed-models here, so the layer changes nothing in what is registered).
func TestProvimuxKeyCompatEntryIsUpstreamsMatch(t *testing.T) {
	entry := func(name, base, model string) config.OpenAICompatibility {
		return config.OpenAICompatibility{Name: name, BaseURL: base,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-acme"}},
			Models:        []config.OpenAICompatibilityModel{{Name: model}}}
	}
	own := entry("acme", "https://own.example.test/v1", "own-model")
	disabled := own
	disabled.Disabled = true
	cases := []struct {
		name    string
		entries []config.OpenAICompatibility
		index   string
		legacy  bool // an older credential: no base_url
		want    string
	}{
		{"index right", []config.OpenAICompatibility{own, entry("other", "https://other.example.test/v1", "other-model")}, "0", false, "own-model"},
		{"stale index, other name", []config.OpenAICompatibility{entry("other", "https://other.example.test/v1", "other-model"), own}, "0", false, "own-model"},
		{"stale index, same name, other base", []config.OpenAICompatibility{entry("acme", "https://other.example.test/v1", "other-model"), own}, "0", false, "own-model"},
		{"index out of range", []config.OpenAICompatibility{own}, "5", false, "own-model"},
		{"endpoint deleted", []config.OpenAICompatibility{entry("other", "https://other.example.test/v1", "other-model")}, "0", false, ""},
		{"endpoint deleted, same name left", []config.OpenAICompatibility{entry("acme", "https://other.example.test/v1", "other-model")}, "0", false, ""},
		{"indexed entry disabled, same endpoint again", []config.OpenAICompatibility{disabled, entry("acme", "https://own.example.test/v1", "again-model")}, "0", false, "again-model"},
		{"same endpoint twice, index on the second", []config.OpenAICompatibility{entry("acme", "https://own.example.test/v1", "first-model"), entry("acme", "https://own.example.test/v1", "second-model")}, "1", false, "second-model"},
		{"legacy credential, stale index", []config.OpenAICompatibility{entry("other", "https://other.example.test/v1", "other-model"), own}, "0", true, "own-model"},
	}
	for _, tc := range cases {
		for _, cached := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "", true: "/cache"}[cached], func(t *testing.T) {
				provimuxLiveReset(t)
				key := keySynthesize(t, &config.Config{OpenAICompatibility: []config.OpenAICompatibility{own}})[0]
				key.Attributes[coreauth.AttributeConfigIndex] = tc.index
				if tc.legacy {
					delete(key.Attributes, "base_url")
				}
				cfg := &config.Config{OpenAICompatibility: tc.entries}
				svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
				key, err := svc.coreManager.Register(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(key.ID) })
				if cached {
					svc.registerModelsForAuthWithCache(context.Background(), key, svc.newOpenAICompatibilityRegistrationCache())
				} else {
					svc.registerModelsForAuth(context.Background(), key)
				}
				var want []string
				if tc.want != "" {
					want = []string{tc.want}
				}
				if got := provimuxRegistered(key); !slices.Equal(got, want) {
					t.Fatalf("upstream registered %v, the case says %v", got, want)
				}
				_, compatName, _ := openAICompatInfoFromAuth(key)
				var picked []string
				if e := keyCompatEntry(cfg, key, compatName); e != nil {
					picked = []string{e.Models[0].Name}
				}
				if !slices.Equal(picked, want) {
					t.Errorf("keyCompatEntry names %v, upstream registered %v", picked, want)
				}
			})
		}
	}
}
