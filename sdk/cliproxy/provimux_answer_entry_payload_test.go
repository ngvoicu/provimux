package cliproxy

import (
	"bytes"
	"context"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvimuxEntryPayloadMatrix(t *testing.T) {
	cfg := sentMatrixConfig + `  interactions:
    - name: provimux-test-interactions
      models:
        - name: provimux-test-interactions-sent
          alias: provimux-test-interactions
      keys:
        - api-key: fixture-interactions
payload:
  override:
    - models:
        - name: "*"
          headers:
            X-Review: change
      params:
        model: review-alternate
  filter:
    - models:
        - name: "*"
          headers:
            X-Review: delete
      params:
        - model
`
	h := newSentHarness(t, cfg)
	useAnsweredBook(t, newSentClock())
	sentLogins(t, h)
	sentCopilot(t, h, "provimux-test-copilot") // ported: a Copilot login the gateway's own login wrote (rule 4)
	for _, tc := range append(sentMatrix(), sentInteractionsCase) {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"plain", "change", "delete"} {
				t.Run(mode, func(t *testing.T) {
					var wire, bodyID, host, path string
					var reached bool
					reviewSet(h, reviewRT(func(r *http.Request) (*http.Response, error) {
						var b []byte
						if r.Body != nil {
							b, _ = io.ReadAll(r.Body)
							r.Body.Close()
						}
						r.Body = io.NopCloser(bytes.NewReader(b))
						if !strings.HasSuffix(r.URL.Path, "/token") {
							reached = true
							wire = standInModel(t, r, b)
							bodyID = ojson.GetBytes(b, "model").String()
							host, path = r.URL.Host, r.URL.Path
						}
						return standInAnswer(r, b, "fixture-vendor-reported"), nil
					}))
					a := h.post(tc.path, tc.body, sentHeader("X-Review", mode))
					if a.status != 200 {
						if a.get(logging.ProvimuxEntryHeader) != "" {
							t.Error("error response gained an entry")
						}
						t.Logf("executor rejected %s, status %d reached=%v", mode, a.status, reached)
						if !reached && mode == "plain" {
							t.Fatal("control did not reach the vendor")
						}
						return
					}
					if !reached {
						t.Fatal("fixture silent success")
					}
					if strings.Contains(path, "/models/") && bodyID != "" && bodyID != wire {
						wire = ""
					}
					want := ""
					if wire != "" && tc.entry != "" {
						p := strings.SplitN(tc.entry, "/", 2)[0]
						switch p {
						case "openrouter":
							want = p + "/" + strings.TrimSuffix(wire, ":nitro")
						case "devin":
							if info := registry.LookupDevinModel(wire); info != nil {
								want = "devin/" + strings.TrimPrefix(info.ID, "devin/")
							}
						default:
							want = p + "/" + wire
						}
					}
					t.Logf("%s %s wire=%q body.model=%q entry=%q", host, path, wire, bodyID, a.get(logging.ProvimuxEntryHeader))
					if a.get(logging.ProvimuxEntryHeader) != want || a.get(logging.ProvimuxModelSentHeader) != wire {
						t.Errorf("received %s, want sent=%q entry=%q, got %v", host, wire, want, a.header)
					}
				})
			}
		})
	}
}

type reviewCatalogErr struct {
	kvstore.KVClient
	fail bool
	n    int
}

func (p *reviewCatalogErr) KVGet(c context.Context, k string) ([]byte, bool, error) {
	if p.fail && strings.HasPrefix(k, "antigravity:model-enums:") {
		p.n++
		return nil, false, fmt.Errorf("fixture catalog read")
	}
	return p.KVClient.KVGet(c, k)
}
func TestProvimuxEntryAntigravityCatalogAndXAIHosts(t *testing.T) {
	t.Run("antigravity-catalog-read-fails", func(t *testing.T) {
		h := newSentHarness(t, "")
		useAnsweredBook(t, newSentClock())
		path := filepath.Join(t.TempDir(), "synthetic.db")
		if e := kvstore.ConfigureLocal(true, path); e != nil {
			t.Fatal(e)
		}
		defer kvstore.ShutdownLocal()
		const g = "provimux-test-stored-group"
		const low = g + "-low"
		const high = g + "-high"
		a := h.register(&ca.Auth{ID: "provimux-test-stored", Provider: "antigravity", Status: ca.StatusActive, FileName: "provimux-test-stored.json", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"type": "antigravity", "access_token": "fixture", "refresh_token": "fixture-r", "project_id": "fixture", "expiry": sentFar(), "expired": sentFar()}}, low)
		if !h.service.coreManager.StoreAntigravityModelCatalog(t.Context(), a, map[string]string{low: "MODEL_PLACEHOLDER_M525", high: "MODEL_PLACEHOLDER_M526"}, map[string]map[string]int{g: {"low": 4096, "high": 8192}}, nil) {
			t.Fatal("catalog")
		}
		k, _, _ := kvstore.CurrentKVClient()
		proxy := &reviewCatalogErr{KVClient: k}
		kvstore.RegisterHomeProvider(func() (kvstore.KVClient, bool, error) { return proxy, true, nil })
		defer kvstore.RegisterHomeProvider(nil)
		for _, bad := range []bool{true, false} {
			proxy.fail = bad
			h.stand.reset()
			h.listsRow(sentClientKey, low, a.ProvimuxAccountMarker())
			if bad && proxy.n != 0 {
				t.Fatal("list consulted catalog")
			}
			ans := h.post("/v1/chat/completions", chatBody(low, false, `,"reasoning_effort":"high"`), nil)
			r := h.stand.answered()
			if ans.status != 200 || len(r) == 0 {
				t.Fatal(ans.status, r)
			}
			want := high
			if bad {
				want = low
			}
			if r[len(r)-1].Model != want || ans.get(logging.ProvimuxEntryHeader) != "antigravity/"+want {
				t.Errorf("catalog failure=%v, receipts=%v, header=%q", bad, r, ans.get(logging.ProvimuxEntryHeader))
			}
			t.Logf("failure=%v read errors=%d receipt=%q header=%q", bad, proxy.n, r[len(r)-1].Model, ans.get(logging.ProvimuxEntryHeader))
		}
	})
	t.Run("xai-key-hosts", func(t *testing.T) {
		for _, tc := range []struct {
			attr, meta, host string
			paid             bool
		}{{"https://api.x.ai/v1", "", "api.x.ai", true}, {"https://api.x.ai/v1", "", "cli-chat-proxy.grok.com", false}, {"", "https://provimux-test-relay.invalid/v1", "provimux-test-relay.invalid", true}} {
			t.Run(tc.host, func(t *testing.T) {
				h := newSentHarness(t, "")
				useAnsweredBook(t, newSentClock())
				a := h.register(&ca.Auth{ID: "provimux-test-sdk-xai", FileName: "provimux-test-sdk-xai.json", Status: ca.StatusActive, Provider: "xai", Attributes: map[string]string{"auth_kind": "apikey", "api_key": "fixture-key", "base_url": tc.attr}, Metadata: map[string]any{"using_api": tc.paid, "base_url": tc.meta}})
				if !h.service.registerModelsForAuth(t.Context(), a) {
					t.Fatal("native catalog registration")
				}
				ans := h.post("/v1/chat/completions", chatBody("grok-4.3", false, ""), nil)
				rs := h.stand.answered()
				if ans.status != 200 || len(rs) != 1 || rs[0].Host != tc.host || rs[0].Model != "grok-4.3" {
					t.Fatal("fixture", ans.status, rs)
				}
				want := ""
				if tc.host == "api.x.ai" {
					want = "xai/grok-4.3"
				}
				if ans.get(logging.ProvimuxEntryHeader) != want {
					t.Errorf("vendor=%v header=%q", rs, ans.get(logging.ProvimuxEntryHeader))
				}
				t.Logf("catalog grok-4.3, vendor=%s header=%q", tc.host, want)
			})
		}
	})
}
