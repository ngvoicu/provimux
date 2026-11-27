package cliproxy

import (
	"bytes"
	"fmt"
	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	sa "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const rowConfig = `api-keys:
  openai-compatibility:
    - name: provimux-test-router
      base-url: https://openrouter.ai/api/v1
      prefix: team
      models:
        - name: lab/a
          alias: pool
        - name: lab/b
          alias: pool
        - name: lab/target:nitro
          alias: forced
          force-mapping: true
      keys:
        - api-key: fixture-router-key
    - name: other-vendor
      base-url: https://api.groq.com/v1
      models:
        - name: pool
      keys:
        - api-key: fixture-groq-key
`

func TestProvimuxAnsweredAliasesAndPrivacy(t *testing.T) {
	for _, only := range []bool{false, true} {
		t.Run(fmt.Sprint(only), func(t *testing.T) {
			cfg := rowConfig + fmt.Sprintf("models:\n  only-my-names:\n    enabled: %v\n", only)
			h := newSentHarness(t, cfg)
			clock := newSentClock()
			useAnsweredBook(t, clock)
			expected := map[string][]string{}
			times := map[string][]time.Time{}
			for _, name := range []string{"team/pool", "team/pool", "team/pool", "team/forced", "forced", "pool", "pool", "pool", "pool", "pool(high)"} {
				// Reader A checks before and after a call by key B. No other synthetic traffic.
				at := clock.Now()
				old := reviewGet(t, h, "/v1/models?include=accounts", sentClientKey)
				clock.add(2 * time.Second)
				h.stand.reset()
				a := h.post("/v1/chat/completions", chatBody(name, false, ""), sentHeader("Authorization", "Bearer "+sentOtherClientKey))
				if a.status != 200 {
					t.Fatalf("fixture %s %d %s", name, a.status, a.body)
				}
				r := h.stand.answered()
				if len(r) != 1 {
					t.Fatalf("fixture %#v", r)
				}
				dest := r[0]
				entry := "openrouter/" + strings.TrimSuffix(dest.Model, ":nitro")
				if dest.Host == "api.groq.com" {
					entry = "groq/" + dest.Model
				}
				if a.get(logging.ProvimuxEntryHeader) != entry {
					t.Fatalf("entry %v vs stand-in %v", a.header, r)
				}
				row := strings.TrimSuffix(name, "(high)")
				tag := a.get(logging.ProvimuxAccountHeader) + " " + row
				lst := []string{entry}
				ts := []time.Time{clock.Now()}
				for i, v := range expected[tag] {
					if v != entry {
						lst = append(lst, v)
						ts = append(ts, times[tag][i])
					}
				}
				expected[tag], times[tag] = lst, ts
				// Per client key: key B's list and the management
				// list give B's answers; reader A's list does not change at all.
				h.wantAnswered(sentOtherClientKey, row, a.get(logging.ProvimuxAccountHeader), ts, lst...)
				after := reviewGet(t, h, "/v1/models?include=accounts", sentClientKey)
				if !bytes.Equal(old, after) {
					t.Fatal("reader A's list reflects key B's call")
				}
				if name == "team/pool" {
					t.Logf("reader bracket %s..%s; visible %v", at.Format(time.TimeOnly), clock.Now().Format(time.TimeOnly), lst)
				}
			}
			for _, acc := range h.service.coreManager.List() {
				for _, row := range []string{"pool", "team/pool", "forced", "team/forced", "lab/a", "lab/b", "lab/target:nitro"} {
					tag := acc.ProvimuxAccountMarker() + " " + row
					cl, mg, _ := h.listsRow(sentOtherClientKey, row, acc.ProvimuxAccountMarker())
					if cl != nil && len(cl.items()) != len(expected[tag]) || mg != nil && len(mg.items()) != len(expected[tag]) {
						t.Errorf("stray history %s %v %v", tag, cl, mg)
					}
					if a, _, _ := h.listsRow(sentClientKey, row, acc.ProvimuxAccountMarker()); a != nil && len(a.items()) != 0 {
						t.Errorf("reader A sees key B's history %s %v", tag, a.items())
					}
				}
			}
		})
	}
}
func TestProvimuxAnsweredHiddenOldName(t *testing.T) {
	cfg := strings.Replace(reviewRouter, "      keys:", "        - name: hidden-before-switch\n      keys:", 1) + "models:\n  answered-by: true\n"
	h := newSentHarness(t, cfg)
	clock := newSentClock()
	useAnsweredBook(t, clock)
	model := "hidden-before-switch"
	ans := h.post("/v1/chat/completions", chatBody(model, false, ""), sentHeader("Authorization", "Bearer "+sentOtherClientKey))
	if ans.status != 200 || ans.get(logging.ProvimuxEntryHeader) != "openrouter/"+model {
		t.Fatalf("before fixture %d %v %s", ans.status, ans.header, ans.body)
	}
	marker := ans.get(logging.ProvimuxAccountHeader)
	// Ported (per client key): the answer was to the other key; its list and the management list have it.
	h.wantAnswered(sentOtherClientKey, model, marker, []time.Time{clock.Now()}, "openrouter/"+model)
	credential := sentKeyFor(t, h, model).ID
	h.put("models/only-my-names", `{"enabled":true,"keep":[]}`)
	h.await("on", map[string][]string{"review-router": {"review-public"}})
	if len(ca.ProvimuxAnswered(ca.ProvimuxClientRef(sentOtherClientKey), credential, marker, model)) == 0 {
		t.Fatal("fixture history was erased")
	}
	for _, key := range []string{sentClientKey, sentOtherClientKey} {
		for _, tail := range []string{"", "?include=accounts", "?include=figures", "?include=accounts,figures", "?include=figures&include=accounts"} {
			b := reviewGet(t, h, "/v1/models"+tail, key)
			if bytes.Contains(b, []byte(model)) {
				t.Errorf("%s exposes the hidden row or entry: %s", tail, b)
			}
		}
	}
	if b := reviewGet(t, h, "/v8/management/provimux/model-routes", namesManagementKey); bytes.Contains(b, []byte(model)) {
		t.Errorf("hidden name in model-routes: %s", b)
	}
	if ans := h.post("/v1/chat/completions", chatBody(model, false, ""), nil); ans.status == 200 {
		t.Fatal("hidden name was served")
	}
}
func TestProvimuxAnsweredCaseDistinctRows(t *testing.T) {
	h := newSentHarness(t, `api-keys:
  openai-compatibility:
    - name: case-fixture
      base-url: https://openrouter.ai/v1
      models:
        - name: Lab/Model
        - name: lab/model
      keys:
        - api-key: fixture-key
`)
	useAnsweredBook(t, newSentClock())
	a := h.post("/v1/chat/completions", chatBody("Lab/Model", false, ""), nil)
	if a.status != 200 {
		t.Fatalf("fixture %d %s", a.status, a.body)
	}
	m := a.get(logging.ProvimuxAccountHeader)
	for _, name := range []string{"Lab/Model", "lab/model"} {
		c, g, _ := h.listsRow(sentClientKey, name, m)
		if c == nil || g == nil {
			t.Logf("stock YAML does not produce separate %s row in both lists", name)
			continue
		}
		t.Logf("asked Lab/Model, %s client=%v management=%v", name, c.items(), g.items())
		if name == "lab/model" && (len(c.items()) > 0 || len(g.items()) > 0) {
			t.Error("the other case-sensitive id gained an answer it never gave")
		}
	}
}
func TestProvimuxAnsweredSameMarkerRows(t *testing.T) {
	h := newSentHarness(t, "")
	useAnsweredBook(t, newSentClock())
	add := func(id, base, priority string) *ca.Auth {
		return h.register(&ca.Auth{ID: id + ".json", FileName: id + ".json", Provider: "meta", Status: ca.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "priority": priority, "path": "/fixture/" + id + ".json"}, Metadata: map[string]any{"type": "meta", "email": "one@fixture.invalid", "api_key": id, "base_url": base}}, "shared-fixture")
	}
	a := add("review-primary", "https://api.meta.ai", "9")
	b := add("review-secondary", "https://other.fixture.invalid", "0")
	if a.ProvimuxAccountMarker() != b.ProvimuxAccountMarker() {
		t.Fatal("fixture markers differ")
	}
	ans := h.post("/v1/chat/completions", chatBody("shared-fixture", false, ""), nil)
	r := h.stand.answered()
	if ans.status != 200 || len(r) != 1 || r[0].Host != "api.meta.ai" {
		t.Fatalf("fixture %d %v", ans.status, r)
	}
	for _, p := range []struct{ path, key, keyRows string }{{"/v1/models?include=accounts", sentClientKey, "data"}, {"/v8/management/provimux/model-routes", namesManagementKey, "models"}} {
		raw := reviewGet(t, h, p.path, p.key)
		matched := 0
		for _, m := range ojson.GetBytes(raw, p.keyRows).Array() {
			if m.Get("id").String() != "shared-fixture" {
				continue
			}
			rows := m.Get("routes")
			if !rows.Exists() {
				rows = m.Get("accounts")
			}
			for _, row := range rows.Array() {
				matched++
				t.Logf("%s priority=%s entries=%s", p.keyRows, row.Get("priority").String(), row.Get("answered").Raw)
				if row.Get("priority").Int() == 0 && len(row.Get("answered").Array()) != 0 {
					t.Errorf("unused credential row carries the primary's entry")
				}
			}
		}
		if matched != 2 {
			t.Fatalf("fixture %d rows", matched)
		}
	}
}
func TestProvimuxAnsweredPluginOriginsAndNativeReplacement(t *testing.T) {
	h := newSentHarness(t, "")
	useAnsweredBook(t, newSentClock())
	dir := t.TempDir()
	store := sa.NewFileTokenStore()
	store.SetBaseDir(dir)
	host := pluginhost.New()
	mk := func(provider, file string) *ca.Auth {
		return host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: provider, FileName: file, Metadata: map[string]any{"type": provider, "auth_kind": "oauth", "api_key": "fixture-token", "access_token": "fixture-access", "email": "fixture@fixture.invalid", "keep_setting": "sentinel"}}, filepath.Join(dir, file), file)
	}
	t.Run("plugin-login", func(t *testing.T) {
		a := mk("meta", "made.json")
		if !pluginhost.ProvimuxPluginCredential(a) {
			t.Fatal("fixture origin")
		}
		if _, err := store.Save(ca.WithAuthCreationIntent(t.Context()), a); err != nil {
			t.Fatal(err)
		}
		a = h.register(a, "plugin-login-model")
		first := h.post("/v1/chat/completions", chatBody("plugin-login-model", false, ""), nil)
		if first.status != 200 || first.get(logging.ProvimuxEntryHeader) != "" {
			t.Fatalf("fixture first %d %v", first.status, first.header)
		}
		reloaded, err := store.List(t.Context())
		if err != nil || len(reloaded) != 1 {
			t.Fatalf("reload %v %d", err, len(reloaded))
		}
		r := reloaded[0]
		if r.Provider != "meta" {
			t.Fatal("fixture provider")
		}
		h2 := newSentHarness(t, "")
		t.Logf("reloaded provider=%s storage=%T origin=%v mark=%v", r.Provider, r.Storage, pluginhost.ProvimuxPluginCredential(r), r.Metadata[ca.ProvimuxPluginCredentialMark])
		h2.register(r, "plugin-login-model")
		a2 := h2.post("/v1/chat/completions", chatBody("plugin-login-model", false, ""), nil)
		if a2.status != 200 || len(h2.stand.answered()) != 1 {
			t.Fatalf("fixture %d %s", a2.status, a2.body)
		}
		t.Logf("plugin login before=%q after=%q entry=%q", first.get(logging.ProvimuxAccountHeader), a2.get(logging.ProvimuxAccountHeader), a2.get(logging.ProvimuxEntryHeader))
		if a2.get(logging.ProvimuxEntryHeader) != "" {
			t.Error("native reload priced a login created by a plugin")
		}
	})
	for _, provider := range []string{"meta", "codex"} {
		t.Run(provider+"-replace", func(t *testing.T) {
			f := provider + ".json"
			a := mk(provider, f)
			path, _ := store.Save(ca.WithAuthCreationIntent(t.Context()), a)
			before, _ := os.ReadFile(path)
			// A plugin's credential carries its mark from the moment
			// it is made, so the first save has it, and marking again changes no byte.
			if ojson.GetBytes(before, ca.ProvimuxPluginCredentialMark).Bool() != true {
				t.Fatal("a plugin-made credential's first save lacks the mark")
			}
			pluginhost.ProvimuxMarkPluginCredential(a)
			if _, err := store.Save(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			var v, w map[string]any
			json.Unmarshal(before, &v)
			json.Unmarshal(after, &w)
			if w[ca.ProvimuxPluginCredentialMark] != true {
				t.Fatal("mark not persisted")
			}
			excise := regexp.MustCompile(`,\s*"provimux_plugin_credential"\s*:\s*true`)
			t.Logf("%s marking again changed no byte=%v", provider, bytes.Equal(after, before))
			if !bytes.Equal(after, before) || excise.Match(nil) {
				t.Error("existing file bytes changed")
			}
			if !reflect.DeepEqual(v, w) {
				t.Fatalf("other data changed: %v %v", v, w)
			}
			native := &ca.Auth{ID: f, FileName: f, Provider: provider, Metadata: map[string]any{"type": provider, "email": "new@fixture.invalid", "auth_kind": "oauth"}}
			if provider == "meta" {
				native.Storage = &meta.MetaTokenStorage{AccessToken: "native-access", APIKey: "native-key", Email: "new@fixture.invalid"}
			} else {
				native.Storage = &codex.CodexTokenStorage{AccessToken: "native-access", RefreshToken: "native-refresh", Email: "new@fixture.invalid"}
			}
			if _, err := store.Save(ca.WithAuthCreationIntent(t.Context()), native); err != nil {
				t.Fatal(err)
			}
			after, _ = os.ReadFile(path)
			if ojson.GetBytes(after, "access_token").String() != "native-access" {
				t.Fatal("native replacement did not happen")
			}
			marked := ojson.GetBytes(after, ca.ProvimuxPluginCredentialMark).Bool()
			t.Logf("%s explicit native creation rewrote credentials, mark retained=%v", provider, marked)
			if provider == "codex" {
				records, e := store.List(t.Context())
				if e != nil {
					t.Fatal(e)
				}
				var reloaded *ca.Auth
				for _, item := range records {
					if item.FileName == f {
						reloaded = item
					}
				}
				if reloaded == nil {
					t.Fatal("missing native replacement")
				}
				sent := ex.ProvimuxSentRequest{Scheme: "https", Host: "chatgpt.com", Model: "fixture-native-codex"}
				price := provimuxEntryOfAnswer(reloaded, sent)
				clean := reloaded.Clone()
				delete(clean.Metadata, ca.ProvimuxPluginCredentialMark)
				control := provimuxEntryOfAnswer(clean, sent)
				t.Logf("native Codex reload entry=%q; same reopened credential with only mark removed=%q", price, control)
				if control != "openai-codex/fixture-native-codex" {
					t.Fatal("fixture native control", control)
				}
				if price != control {
					t.Error("previous plugin update suppresses genuine replacement's entry")
				}
			}
			if marked {
				t.Error("native replacement retained plugin marker")
			}
		})
	}
}

func TestProvimuxAnsweredExpiryAndOrder(t *testing.T) {
	c := newSentClock()
	b := newProvimuxAnsweredBook(c.Now)
	const k, cr, m = "k.client", "c.credential", "a.aaaaaaaaaaaaaaaaaaaa"
	b.note(k, cr, m, "name", "p/old")
	c.add(90*24*time.Hour - time.Nanosecond)
	if len(b.read(k, false, cr, m, "name")) != 1 {
		t.Fatal("early expiry")
	}
	c.add(time.Nanosecond)
	if len(b.read(k, false, cr, m, "name")) != 1 {
		t.Fatal("exact boundary")
	}
	c.add(time.Nanosecond)
	if len(b.read(k, false, cr, m, "name")) != 0 {
		t.Fatal("late expiry")
	}
	// First observation pauses after it reads the clock. A newer answer is noted meanwhile. (Ported: the second note
	// runs on a goroutine of its own, since the clock is now read under the lock the first note holds.)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	base := c.Now()
	b = newProvimuxAnsweredBook(func() time.Time {
		n := calls.Add(1)
		if n == 1 {
			close(entered)
			<-release
		}
		return base.Add(time.Duration(n) * time.Second)
	})
	done := make(chan struct{})
	go func() { b.note(k, cr, m, "n", "p/first"); close(done) }()
	<-entered
	second := make(chan struct{})
	go func() { b.note(k, cr, m, "n", "p/second"); close(second) }()
	close(release)
	<-done
	<-second
	r := b.read(k, false, cr, m, "n")
	t.Logf("concurrent order: %v", r)
	if !sort.SliceIsSorted(r, func(i, j int) bool { return r[i].At.After(r[j].At) }) {
		t.Error("both lists retain an older timestamp first")
	}
}

func TestProvimuxAnsweredOffHomeBytes(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	useAnsweredBook(t, newSentClock())
	a := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
	if a.status != 200 {
		t.Fatal(a.status)
	}
	take := func() map[string]string {
		r := map[string]string{}
		r["plain"] = string(reviewGet(t, h, "/v1/models", sentClientKey))
		r["accounts"] = string(reviewGet(t, h, "/v1/models?include=accounts", sentClientKey))
		r["management"] = string(reviewGet(t, h, "/v8/management/provimux/model-routes", namesManagementKey))
		for _, stream := range []bool{false, true} {
			ans := h.post("/v1/chat/completions", chatBody("review-public", stream, ""), nil)
			for k := range ans.header {
				if strings.HasPrefix(strings.ToLower(k), "x-provimux-") {
					t.Error("header", k)
				}
			}
			r[fmt.Sprint(stream)] = ans.body
		}
		return r
	}
	logging.SetProvimuxAnsweredBy(false)
	off := take()
	cfg := *h.service.currentConfig()
	cfg.Home.Enabled = true
	on := true
	cfg.Models.AnsweredBy = &on
	provimuxSetAnsweredBy(&cfg)
	home := take()
	for k, v := range off {
		w := home[k]
		if k == "management" {
			v = strings.Replace(v, ojson.Get(v, "observed_at").String(), "clock", 1)
			w = strings.Replace(w, ojson.Get(w, "observed_at").String(), "clock", 1)
		}
		if v != w {
			t.Errorf("bytes %s differ (%d vs %d)", k, len(v), len(w))
		}
		if strings.Contains(w, `"answered"`) {
			t.Errorf("history on %s", k)
		}
	}
}

var _ = http.MethodGet
var _ = config.Config{}

func TestProvimuxAnsweredHiddenCaseTwin(t *testing.T) {
	h := newSentHarness(t, strings.Replace(reviewRouter, "      keys:", "        - name: Lab/Model\n        - name: lab/model\n      keys:", 1)+"models:\n  answered-by: true\n")
	useAnsweredBook(t, newSentClock())
	a := h.post("/v1/chat/completions", chatBody("Lab/Model", false, ""), sentHeader("Authorization", "Bearer "+sentOtherClientKey))
	if a.status != 200 || a.get(logging.ProvimuxEntryHeader) != "openrouter/Lab/Model" {
		t.Fatalf("fixture before %d %v", a.status, a.header)
	}
	h.put("models/only-my-names", `{"enabled":true,"keep":["lab/model"]}`)
	h.await("on", map[string][]string{"review-router": {"lab/model", "review-public"}})
	refused := h.post("/v1/chat/completions", chatBody("Lab/Model", false, ""), nil)
	if refused.status == 200 {
		t.Fatal("fixture: upper case must be unserved")
	}
	for _, key := range []string{sentClientKey, sentOtherClientKey} {
		c, m, raw := h.listsRow(key, "lab/model", a.get(logging.ProvimuxAccountHeader))
		if c == nil || m == nil {
			t.Fatal("no allowed row", raw)
		}
		t.Logf("hidden request HTTP %d, served lower-case row received zero calls; client=%v management=%v", refused.status, c.items(), m.items())
		if len(c.items()) > 0 || len(m.items()) > 0 {
			t.Error("a served row exposes the unserved name's historical entry")
		}
	}
}
