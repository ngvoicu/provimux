package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/sessionroute"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// Provimux: the client paths of session-route through the real router (.github/provimux/design/session-route.md 12,
// rows 5, 6 and 12 for the client paths). Every key, account and session is invented; the executor is a stand-in in
// this process and nothing is sent anywhere. Times are on the clock the test sets.

const srAPIModel = "provimux-sr-api-model"

// srAPIExecutor answers every request; a high account fails while failHigh is set.
type srAPIExecutor struct {
	provider string
	failHigh *bool
}

func (e srAPIExecutor) Identifier() string { return e.provider }

func (e srAPIExecutor) Execute(ctx context.Context, a *auth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if e.failHigh != nil && *e.failHigh && strings.Contains(a.ID, "high") {
		return cliproxyexecutor.Response{}, &auth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"id":"sr","object":"chat.completion","created":1,"model":"` + srAPIModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)}, nil
}

func (e srAPIExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	resp, err := e.Execute(ctx, a, req, opts)
	if err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e srAPIExecutor) CountTokens(ctx context.Context, a *auth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, a, req, opts)
}

func (srAPIExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) { return a, nil }

func (srAPIExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, &auth.Error{Code: "not_supported", Message: "stand-in"}
}

type srAPIFixture struct {
	t        *testing.T
	server   *Server
	manager  *auth.Manager
	failHigh bool
}

// newSRAPIFixture is a server with two client keys and two accounts for one name: high (10, a subscription login) and
// low (5, an API key with a base URL), session affinity on, the session-route book started.
func newSRAPIFixture(t *testing.T) *srAPIFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	restoreTests := sessionroute.EnableInTests()
	t.Cleanup(restoreTests)
	f := &srAPIFixture{t: t}
	provider := "provimux-sr-api-" + strings.ReplaceAll(t.Name(), "/", "-")
	manager := auth.NewManager(nil, nil, nil)
	affinity := auth.NewSessionAffinitySelectorWithConfig(auth.SessionAffinityConfig{Fallback: &auth.RoundRobinSelector{}, TTL: time.Hour})
	t.Cleanup(affinity.Stop)
	manager.SetSelector(affinity)
	manager.RegisterExecutor(srAPIExecutor{provider: provider, failHigh: &f.failHigh})
	for _, a := range []*auth.Auth{
		{ID: provider + "-high", Provider: provider, Status: auth.StatusActive, Attributes: map[string]string{"priority": "10"},
			Metadata: map[string]any{"type": provider, "access_token": "fixture-token-high", "account_id": "fixture-account-high"}},
		{ID: provider + "-low", Provider: provider, Status: auth.StatusActive, Attributes: map[string]string{"priority": "5", "api_key": "sk-fixture-low", "base_url": "https://low.example.test:8443/v1"}},
	} {
		if _, err := manager.Register(auth.WithSkipPersist(context.Background()), a); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, provider, []*registry.ModelInfo{{ID: srAPIModel, Object: "model", OwnedBy: "provimux-test", Created: 1}})
		id := a.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	access := sdkaccess.NewManager()
	tmp := t.TempDir()
	cfg := &proxyconfig.Config{}
	cfg.APIKeys = []string{"sk-sr-one", "sk-sr-two"}
	cfg.Port = 0
	cfg.AuthDir = filepath.Join(tmp, "auth")
	cfg.Debug = true
	f.server = NewServer(cfg, manager, access, filepath.Join(tmp, "config.yaml"))
	f.manager = manager
	return f
}

// do sends one request and returns the status and the body.
func (f *srAPIFixture) do(method, path, key string, header http.Header, body string) (int, []byte) {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for name, values := range header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	rec := httptest.NewRecorder()
	f.server.engine.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (f *srAPIFixture) turn(key string, header http.Header) {
	f.t.Helper()
	code, body := f.do(http.MethodPost, "/v1/chat/completions", key, header, `{"model":"`+srAPIModel+`","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		f.t.Fatalf("turn: %d %s", code, body)
	}
}

// recoverHigh records a successful answer of the high account for the name, which ends its cooldown as any
// success does upstream: it is a first route again.
func (f *srAPIFixture) recoverHigh() {
	f.t.Helper()
	for _, a := range f.manager.List() {
		if strings.HasSuffix(a.ID, "-high") {
			f.manager.MarkResult(context.Background(), auth.Result{AuthID: a.ID, Provider: a.Provider, Model: srAPIModel, Success: true})
			return
		}
	}
	f.t.Fatal("no high account")
}

func (f *srAPIFixture) show(key string, header http.Header, body string) (int, []byte) {
	return f.do(http.MethodPost, "/v1/provimux/session-route", key, header, body)
}

func (f *srAPIFixture) release(key string, header http.Header, body string) (int, []byte) {
	return f.do(http.MethodPost, "/v1/provimux/session-route/release", key, header, body)
}

// srAPIClock fixes session-route's clock for the test.
func srAPIClock(t *testing.T, at time.Time) {
	t.Helper()
	restore := auth.SetProvimuxSessionRouteClockForTest(func() time.Time { return at })
	t.Cleanup(restore)
}

// TestProvimuxSessionRouteClientPaths: a conversation that fell back is shown to the key that used it, released by
// it, and the next turn goes to the first route; every answer matches the contract (row 12).
func TestProvimuxSessionRouteClientPaths(t *testing.T) {
	f := newSRAPIFixture(t)
	contract := loadSRContract(t)
	srAPIClock(t, time.Now())
	header := http.Header{"X-Session-Affinity": {"pi-session-0001"}}
	f.turn("sk-sr-one", header)
	f.failHigh = true
	f.turn("sk-sr-one", header) // the high account fails: the conversation falls back to low
	f.failHigh = false

	code, body := f.show("sk-sr-one", header, `{"model":"`+srAPIModel+`"}`)
	contract.check(t, "SessionRoute", code, http.StatusOK, body)
	var show auth.ProvimuxSessionRoute
	mustUnmarshal(t, body, &show)
	if show.Conversation != "bound" || show.Bound.Origin != "fallback" || show.Bound.Route.Priority != 5 || show.Bound.FellBackFrom == nil || show.Bound.FellBackFrom.Priority != 10 {
		t.Fatalf("show: %s", body)
	}

	code, body = f.release("sk-sr-one", header, `{"model":"`+srAPIModel+`","expect":"b.aaaaaaaaaaaaaaaa"}`)
	contract.check(t, "SessionRoute", code, http.StatusConflict, body)

	code, body = f.release("sk-sr-one", header, `{"model":"`+srAPIModel+`","expect":"`+*show.Binding+`"}`)
	contract.check(t, "Release", code, http.StatusOK, body)
	var released auth.ProvimuxSessionRelease
	mustUnmarshal(t, body, &released)
	if !released.Released || released.Was == nil || released.Was.Priority != 5 {
		t.Fatalf("release: %s", body)
	}

	code, body = f.release("sk-sr-one", header, `{"model":"`+srAPIModel+`"}`)
	contract.check(t, "Release", code, http.StatusOK, body)
	code, body = f.show("sk-sr-one", header, `{"model":"`+srAPIModel+`(high)"}`)
	contract.check(t, "SessionRoute", code, http.StatusOK, body)
	if !bytes.Contains(body, []byte(`"conversation":"not-bound"`)) || !bytes.Contains(body, []byte(`"model":"`+srAPIModel+`"`)) {
		t.Fatalf("after the release (thinking suffix cut): %s", body)
	}

	f.recoverHigh()
	f.turn("sk-sr-one", header)
	code, body = f.show("sk-sr-one", header, `{"model":"`+srAPIModel+`"}`)
	contract.check(t, "SessionRoute", code, http.StatusOK, body)
	show = auth.ProvimuxSessionRoute{}
	mustUnmarshal(t, body, &show)
	if show.Bound == nil || show.Bound.Route.Priority != 10 || show.Bound.Origin != "first" {
		t.Fatalf("the next turn after the release: %s", body)
	}

	// A body identity (prompt_cache_key) names a conversation as its turns do.
	code, _ = f.do(http.MethodPost, "/v1/chat/completions", "sk-sr-two", nil, `{"model":"`+srAPIModel+`","prompt_cache_key":"project-a","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	code, body = f.show("sk-sr-two", nil, `{"model":"`+srAPIModel+`","identity":{"prompt_cache_key":"project-a"}}`)
	contract.check(t, "SessionRoute", code, http.StatusOK, body)
	if !bytes.Contains(body, []byte(`"conversation":"bound"`)) {
		t.Fatalf("body identity: %s", body)
	}
}

// TestProvimuxSessionRouteOtherKey is row 5: another key's conversation and one that does not exist get the same
// answer byte for byte, on both paths, and the other key's release removes nothing.
func TestProvimuxSessionRouteOtherKey(t *testing.T) {
	f := newSRAPIFixture(t)
	srAPIClock(t, time.Now())
	header := http.Header{"X-Session-ID": {"main"}}
	f.turn("sk-sr-one", header)
	request := `{"model":"` + srAPIModel + `"}`

	_, theirs := f.show("sk-sr-two", header, request)
	_, none := f.show("sk-sr-two", http.Header{"X-Session-ID": {"nobody-uses-this"}}, request)
	_, unnamed := f.show("sk-sr-two", nil, request)
	if !bytes.Equal(theirs, none) || !bytes.Equal(theirs, unnamed) {
		t.Fatalf("another key's conversation:\n%s\nnone:\n%s\nunnamed:\n%s", theirs, none, unnamed)
	}
	_, releaseTheirs := f.release("sk-sr-two", header, request)
	_, releaseNone := f.release("sk-sr-two", http.Header{"X-Session-ID": {"nobody-uses-this"}}, request)
	if !bytes.Equal(releaseTheirs, releaseNone) || !bytes.Contains(releaseTheirs, []byte(`"conversation":"not-seen"`)) {
		t.Fatalf("release by another key:\n%s\n%s", releaseTheirs, releaseNone)
	}
	if _, mine := f.show("sk-sr-one", header, request); !bytes.Contains(mine, []byte(`"conversation":"bound"`)) {
		t.Fatalf("the owner's conversation after another key's release: %s", mine)
	}
}

// TestProvimuxSessionRouteRevealsNoMore is row 6: every route the show gives is, field for field, the row
// GET /v1/models?include=accounts gives the same account; with models.answered-by on and off.
func TestProvimuxSessionRouteRevealsNoMore(t *testing.T) {
	for _, answeredBy := range []bool{false, true} {
		t.Run(fmt.Sprintf("answered-by=%v", answeredBy), func(t *testing.T) {
			auth.SetProvimuxMarkerKey([]byte("provimux-session-route-test-key!"))
			was := logging.SetProvimuxAnsweredBy(answeredBy)
			t.Cleanup(func() { logging.SetProvimuxAnsweredBy(was); auth.SetProvimuxMarkerKey(nil) })

			f := newSRAPIFixture(t)
			header := http.Header{"X-Session-Affinity": {"reveal-0001"}}
			f.turn("sk-sr-one", header)
			f.failHigh = true
			f.turn("sk-sr-one", header)
			f.failHigh = false
			f.recoverHigh() // the high account is a first route again

			code, list := f.do(http.MethodGet, "/v1/models?include=accounts", "sk-sr-one", nil, "")
			if code != http.StatusOK {
				t.Fatal(code)
			}
			rows := map[int]string{}
			var models struct {
				Data []struct {
					ID       string            `json:"id"`
					Accounts []json.RawMessage `json:"accounts"`
				} `json:"data"`
			}
			mustUnmarshal(t, list, &models)
			for _, model := range models.Data {
				if model.ID != srAPIModel {
					continue
				}
				for _, row := range model.Accounts {
					var r struct {
						Priority int `json:"priority"`
					}
					mustUnmarshal(t, row, &r)
					rows[r.Priority] = string(row)
				}
			}
			if len(rows) != 2 {
				t.Fatalf("include=accounts rows: %v (%s)", rows, list)
			}
			_, body := f.show("sk-sr-one", header, `{"model":"`+srAPIModel+`"}`)
			var show struct {
				Bound struct {
					Route        json.RawMessage `json:"route"`
					FellBackFrom json.RawMessage `json:"fell_back_from"`
				} `json:"bound"`
				First []json.RawMessage `json:"first"`
			}
			mustUnmarshal(t, body, &show)
			routes := append([]json.RawMessage{show.Bound.Route, show.Bound.FellBackFrom}, show.First...)
			if len(routes) != 3 {
				t.Fatalf("show: %s", body)
			}
			for _, route := range routes {
				var r struct {
					Priority int `json:"priority"`
				}
				mustUnmarshal(t, route, &r)
				if !sameJSON(t, route, []byte(rows[r.Priority])) {
					t.Fatalf("route %s is not the include=accounts row %s", route, rows[r.Priority])
				}
			}
			hasAccount := bytes.Contains(show.Bound.Route, []byte(`"account"`))
			if hasAccount != answeredBy {
				t.Fatalf("account given %v with answered-by %v: %s", hasAccount, answeredBy, show.Bound.Route)
			}
			for _, leak := range []string{"fixture-account-high", "fixture-token-high", "sk-fixture-low", "8443", "/v1", "-high", "-low", "auth_index", "cooldown"} {
				if bytes.Contains(body, []byte(leak)) {
					t.Fatalf("the show gives %q: %s", leak, body)
				}
			}
		})
	}
}

// TestProvimuxSessionRouteRequests: what the paths refuse, in the /v1 group's shapes: no key (401), no model, an
// unknown field, expect on a show, an identity that is not an object, and a name "serve only my names" refuses.
func TestProvimuxSessionRouteRequests(t *testing.T) {
	f := newSRAPIFixture(t)
	contract := loadSRContract(t)
	for _, c := range []struct {
		path, key, body string
		want            int
	}{
		{"/v1/provimux/session-route", "", `{"model":"m"}`, http.StatusUnauthorized},
		{"/v1/provimux/session-route", "sk-unknown", `{"model":"m"}`, http.StatusUnauthorized},
		{"/v1/provimux/session-route/release", "", `{"model":"m"}`, http.StatusUnauthorized},
		{"/v1/provimux/session-route", "sk-sr-one", `{}`, http.StatusBadRequest},
		{"/v1/provimux/session-route", "sk-sr-one", `{"model":" "}`, http.StatusBadRequest},
		{"/v1/provimux/session-route", "sk-sr-one", `{"model":"m","extra":1}`, http.StatusBadRequest},
		{"/v1/provimux/session-route", "sk-sr-one", `{"model":"m","expect":"b.x"}`, http.StatusBadRequest},
		{"/v1/provimux/session-route", "sk-sr-one", `{"model":"m","identity":"pck"}`, http.StatusBadRequest},
		{"/v1/provimux/session-route", "sk-sr-one", `not json`, http.StatusBadRequest},
		{"/v1/provimux/session-route/release", "sk-sr-one", `{"model":"m","expect":1}`, http.StatusBadRequest},
	} {
		code, body := f.do(http.MethodPost, c.path, c.key, nil, c.body)
		contract.check(t, "Error", code, c.want, body)
	}

	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	rule := onlymynames.NewRule(true, false)
	rule.AddName("a-served-name", onlymynames.SetByAlias)
	registry.SetProvimuxNamesRule(rule)
	code, body := f.show("sk-sr-one", nil, `{"model":"`+srAPIModel+`"}`)
	contract.check(t, "Error", code, http.StatusBadRequest, body)
	if !bytes.Contains(body, []byte("models.only-my-names")) {
		t.Fatalf("not served: %s", body)
	}
}

// TestProvimuxSessionRouteNotRunningPaths: affinity off, Home mode and a plugin scheduler, through the paths; each
// answer says why, as the contract's examples do.
func TestProvimuxSessionRouteNotRunningPaths(t *testing.T) {
	contract := loadSRContract(t)
	for _, mode := range []string{"off", "home", "not-supported"} {
		t.Run(mode, func(t *testing.T) {
			f := newSRAPIFixture(t)
			switch mode {
			case "off":
				f.manager.SetSelector(&auth.RoundRobinSelector{})
			case "home":
				cfg := &proxyconfig.Config{}
				cfg.Home.Enabled = true
				f.manager.SetConfig(cfg)
			case "not-supported":
				f.manager.SetPluginScheduler(srAPIScheduler{})
			}
			code, body := f.show("sk-sr-one", http.Header{"X-Session-Affinity": {"x"}}, `{"model":"`+srAPIModel+`"}`)
			contract.check(t, "SessionRoute", code, http.StatusOK, body)
			if !bytes.Contains(body, []byte(`"affinity":"`+mode+`"`)) || !bytes.Contains(body, []byte(`"conversation":"not-bound"`)) {
				t.Fatalf("show: %s", body)
			}
			code, body = f.release("sk-sr-one", http.Header{"X-Session-Affinity": {"x"}}, `{"model":"`+srAPIModel+`"}`)
			contract.check(t, "Release", code, http.StatusOK, body)
			if !bytes.Contains(body, []byte(`"affinity":"`+mode+`"`)) || !bytes.Contains(body, []byte(`"released":false`)) {
				t.Fatalf("release: %s", body)
			}
		})
	}
}

type srAPIScheduler struct{}

func (srAPIScheduler) PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	return pluginapi.SchedulerPickResponse{}, false, nil
}

// TestProvimuxSessionRouteContractExamples is row 12's other half: every example of the two client answers in the
// contract is valid against its schema, and goes through the build's own types unchanged, so the build writes the
// contract's shape: every field, nothing more, null where the contract says null.
func TestProvimuxSessionRouteContractExamples(t *testing.T) {
	contract := loadSRContract(t)
	for name, schema := range map[string]any{
		"FellBackCacheCold": &auth.ProvimuxSessionRoute{}, "FellBackFirstRouteCooling": &auth.ProvimuxSessionRoute{},
		"OnFirstRoute": &auth.ProvimuxSessionRoute{}, "NotSeen": &auth.ProvimuxSessionRoute{}, "AffinityOff": &auth.ProvimuxSessionRoute{},
		"BoundToUnlisted": &auth.ProvimuxSessionRoute{},
		"Released":        &auth.ProvimuxSessionRelease{}, "NothingBound": &auth.ProvimuxSessionRelease{},
		"ReleaseNotSeen": &auth.ProvimuxSessionRelease{}, "ReleaseAffinityOff": &auth.ProvimuxSessionRelease{},
	} {
		example := contract.example(t, name)
		schemaName := "SessionRoute"
		if _, ok := schema.(*auth.ProvimuxSessionRelease); ok {
			schemaName = "Release"
		}
		contract.check(t, schemaName, http.StatusOK, http.StatusOK, example)
		mustUnmarshal(t, example, schema)
		back, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, example, back) {
			t.Fatalf("example %s through the build's type:\n%s\n%s", name, example, back)
		}
	}
}

// --- the contract --------------------------------------------------------------------------------------------

type srContract struct {
	schemas  map[string]any
	examples map[string]any
}

func loadSRContract(t *testing.T) srContract {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "provimux", "design", "session-route", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	components := doc["components"].(map[string]any)
	return srContract{schemas: components["schemas"].(map[string]any), examples: components["examples"].(map[string]any)}
}

func (c srContract) example(t *testing.T, name string) []byte {
	t.Helper()
	example, ok := c.examples[name].(map[string]any)
	if !ok {
		t.Fatalf("no example %s", name)
	}
	raw, err := json.Marshal(example["value"])
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// check requires the status, then validates the body against the named schema.
func (c srContract) check(t *testing.T, schema string, code, want int, body []byte) {
	t.Helper()
	if code != want {
		t.Fatalf("status %d, want %d: %s", code, want, body)
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	if problems := c.validate(map[string]any{"$ref": "#/components/schemas/" + schema}, value, "$"); len(problems) > 0 {
		t.Fatalf("%s does not match %s:\n%s\n%s", body, schema, strings.Join(problems, "\n"), "")
	}
}

// validate is the part of JSON Schema the contract uses: $ref, type (with null), required, properties (a property
// the schema does not name is a problem for the build's own answers), enum, oneOf, allOf, pattern, minimum,
// format date-time.
func (c srContract) validate(schema map[string]any, value any, at string) []string {
	if ref, ok := schema["$ref"].(string); ok {
		return c.validate(c.schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any), value, at)
	}
	if oneOf, ok := schema["oneOf"].([]any); ok {
		matched := 0
		for _, option := range oneOf {
			if len(c.validate(option.(map[string]any), value, at)) == 0 {
				matched++
			}
		}
		if matched != 1 {
			return []string{fmt.Sprintf("%s: matches %d of oneOf", at, matched)}
		}
		return nil
	}
	var problems []string
	if allOf, ok := schema["allOf"].([]any); ok {
		for _, part := range allOf {
			problems = append(problems, c.validate(part.(map[string]any), value, at)...)
		}
	}
	if types := srTypes(schema["type"]); len(types) > 0 && !srTypeIs(types, value) {
		return append(problems, fmt.Sprintf("%s: %v is not %v", at, value, types))
	}
	if value == nil {
		return problems
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(value) {
				found = true
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("%s: %v not in %v", at, value, enum))
		}
	}
	if s, ok := value.(string); ok {
		if pattern, ok := schema["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(s) {
			problems = append(problems, fmt.Sprintf("%s: %q does not match %s", at, s, pattern))
		}
		if schema["format"] == "date-time" {
			if _, err := time.Parse(time.RFC3339, s); err != nil || !strings.HasSuffix(s, "Z") {
				problems = append(problems, fmt.Sprintf("%s: %q is not an RFC 3339 UTC time", at, s))
			}
		}
	}
	if n, ok := value.(float64); ok {
		if minimum, ok := schema["minimum"].(int); ok && n < float64(minimum) {
			problems = append(problems, fmt.Sprintf("%s: %v under %d", at, n, minimum))
		}
	}
	if object, ok := value.(map[string]any); ok {
		properties, _ := schema["properties"].(map[string]any)
		for _, name := range srStrings(schema["required"]) {
			if _, present := object[name]; !present {
				problems = append(problems, fmt.Sprintf("%s: %s is required", at, name))
			}
		}
		if properties != nil && schema["additionalProperties"] != true {
			names := make([]string, 0, len(object))
			for name := range object {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				property, known := properties[name]
				if !known {
					problems = append(problems, fmt.Sprintf("%s: %s is not in the contract", at, name))
					continue
				}
				problems = append(problems, c.validate(property.(map[string]any), object[name], at+"."+name)...)
			}
		}
	}
	if array, ok := value.([]any); ok {
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range array {
				problems = append(problems, c.validate(items, item, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	}
	return problems
}

func srTypes(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		return srStrings(t)
	}
	return nil
}

func srStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func srTypeIs(types []string, value any) bool {
	for _, typ := range types {
		switch typ {
		case "null":
			if value == nil {
				return true
			}
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "integer":
			if n, ok := value.(float64); ok && n == float64(int64(n)) {
				return true
			}
		case "number":
			if _, ok := value.(float64); ok {
				return true
			}
		case "object":
			if _, ok := value.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := value.([]any); ok {
				return true
			}
		}
	}
	return false
}

func mustUnmarshal(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	mustUnmarshal(t, a, &x)
	mustUnmarshal(t, b, &y)
	return reflect.DeepEqual(x, y)
}
