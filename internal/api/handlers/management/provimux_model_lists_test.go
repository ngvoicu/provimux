package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

// provimuxFakeModelListSource stands in for sdk/cliproxy.
type provimuxFakeModelListSource struct {
	states map[string]ProvimuxModelListLogin // by auth ID; a missing ID gets no row
	asked  []string
}

func (*provimuxFakeModelListSource) LoginTypes() []string {
	return []string{"claude", "devin", "vertex"}
}

func (s *provimuxFakeModelListSource) ModelListLogin(auth *coreauth.Auth, liveListsOn, withModels, withLive bool) (ProvimuxModelListLogin, bool) {
	s.asked = append(s.asked, fmt.Sprintf("%s:%t:%t:%t", auth.ID, liveListsOn, withModels, withLive))
	state, ok := s.states[auth.ID]
	if !ok {
		return ProvimuxModelListLogin{}, false
	}
	if !withModels {
		state.Models = nil
	}
	if !withLive {
		state.Live = nil
	}
	return state, true
}

func provimuxUseModelListSource(t *testing.T, source ProvimuxModelListSource) {
	t.Helper()
	previous := currentProvimuxModelListSource()
	SetProvimuxModelListSource(source)
	t.Cleanup(func() { SetProvimuxModelListSource(previous) })
}

func provimuxGetModelLists(t *testing.T, h *Handler, query string, want int) (map[string]any, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/model-lists"+query, nil)
	h.ProvimuxModelLists(ctx)
	if recorder.Code != want {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, want, recorder.Body.String())
	}
	var body map[string]any
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	return body, recorder.Body.Bytes()
}

func provimuxModelListFixture(t *testing.T) (*Handler, *provimuxFakeModelListSource) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range []*coreauth.Auth{
		{ID: "devin-ada.json", Provider: "devin", FileName: "devin-ada.json", Label: "office@providers.dev",
			Attributes: map[string]string{"auth_kind": "oauth", "path": "/auths/devin-ada.json"}, Metadata: map[string]any{"email": "office@providers.dev"}},
		{ID: "vertex-project.json", Provider: "vertex", FileName: "vertex-project.json", Label: "vertex-project",
			Attributes: map[string]string{"path": "/auths/vertex-project.json"}},
		{ID: "claude-key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-test"}},
	} {
		if _, err := manager.Register(t.Context(), auth); err != nil {
			t.Fatal(err)
		}
	}
	source := &provimuxFakeModelListSource{states: map[string]ProvimuxModelListLogin{
		"devin-ada.json": {Source: "live", Supported: true, Held: true, FetchedAt: provimuxFetchedAt, Listed: 2, Learned: 1,
			Failure: &ProvimuxLiveListFailure{At: provimuxFetchedAt.Add(time.Hour), Category: "http_status", HTTPStatus: 401},
			Models: []ProvimuxModelListModel{
				{ID: "devin/swe-2", Name: "SWE-2", Source: "live", Context: 262000, Output: 128000, Inputs: []string{"text"},
					Levels: []string{"medium"}, Sources: map[string]string{"context": "live", "reasoning_levels": "built-in"}},
				{ID: "devin/gemini-4-neon", Source: "live", Learned: true},
			},
			Live: []ProvimuxVendorModel{{ID: "devin/swe-2", Name: "SWE-2", Context: 262000}, {ID: "devin/gemini-4-neon"}}},
		"vertex-project.json": {Source: "built-in"},
	}}
	provimuxUseModelListSource(t, source)
	cfg := &config.Config{}
	cfg.Models.LiveLists = new(true)
	return &Handler{cfg: cfg, authManager: manager}, source
}

func TestProvimuxModelListsRows(t *testing.T) {
	h, source := provimuxModelListFixture(t)
	body, raw := provimuxGetModelLists(t, h, "", http.StatusOK)
	logins := body["logins"].([]any)
	if len(logins) != 2 {
		t.Fatalf("rows = %d: the source decides which credentials get one: %s", len(logins), raw)
	}
	devin := logins[0].(map[string]any)
	if devin["provider"] != "devin" || devin["source"] != "live" || devin["models"] != nil || devin["learned_models"].(float64) != 1 {
		t.Fatalf("devin = %v", devin)
	}
	if _, present := devin["live_list"]; present {
		t.Fatal("live_list only with view=live")
	}
	live := devin["live"].(map[string]any)
	if live["fetched_at"] != "2026-11-19T16:10:44Z" || live["listed_models"].(float64) != 2 || live["held"] != true {
		t.Fatalf("live = %v", live)
	}
	failure := live["last_failure"].(map[string]any)
	if failure["category"] != "http_status" || failure["http_status"].(float64) != 401 {
		t.Fatalf("failure = %v", failure)
	}
	vertex := logins[1].(map[string]any)
	vertexLive := vertex["live"].(map[string]any)
	if vertex["source"] != "built-in" || vertexLive["supported"] != false || vertexLive["fetched_at"] != nil || vertexLive["listed_models"] != nil {
		t.Fatalf("vertex = %v", vertex)
	}
	if body["options"].(map[string]any)["live_lists"] != true || body["published"].(map[string]any)["state"] != "off" {
		t.Fatalf("top = %s", raw)
	}
	for _, asked := range source.asked {
		if strings.HasSuffix(asked, ":true:false") || strings.Contains(asked, ":true:true:") {
			t.Fatalf("models or live lists asked without a request for them: %v", source.asked)
		}
	}
	if strings.Contains(string(raw), "sk-test") {
		t.Fatal("no key in the answer")
	}
	provimuxValidate(t, "ModelListsResponse", raw)
}

func TestProvimuxModelListsOneLogin(t *testing.T) {
	h, _ := provimuxModelListFixture(t)
	index := ""
	for _, auth := range h.authManager.List() {
		if auth.ID == "devin-ada.json" {
			index = lockedAuthIndex(auth)
		}
	}
	body, raw := provimuxGetModelLists(t, h, "?auth_index="+index, http.StatusOK)
	logins := body["logins"].([]any)
	if len(logins) != 1 {
		t.Fatalf("only the named login: %s", raw)
	}
	models := logins[0].(map[string]any)["models"].([]any)
	if len(models) != 2 || models[0].(map[string]any)["id"] != "devin/gemini-4-neon" {
		t.Fatalf("models sorted by id: %v", models)
	}
	swe := models[1].(map[string]any)
	sources := swe["sources"].(map[string]any)
	if sources["context"] != "live" || sources["reasoning_levels"] != "built-in" || sources["output"] != nil {
		t.Fatalf("sources = %v", sources)
	}
	neon := models[0].(map[string]any)
	if neon["learned"] != true || neon["display_name"] != nil || neon["limits"].(map[string]any)["context"] != nil {
		t.Fatalf("neon = %v", neon)
	}
	provimuxValidate(t, "ModelListsResponse", raw)
	none, _ := provimuxGetModelLists(t, h, "?auth_index=0000000000000000", http.StatusOK)
	if len(none["logins"].([]any)) != 0 {
		t.Fatal("an unknown auth_index returns no rows")
	}
}

func TestProvimuxModelListsViewLive(t *testing.T) {
	h, _ := provimuxModelListFixture(t)
	body, raw := provimuxGetModelLists(t, h, "?view=live", http.StatusOK)
	logins := body["logins"].([]any)
	devin, vertex := logins[0].(map[string]any), logins[1].(map[string]any)
	list, ok := devin["live_list"].([]any)
	if !ok || len(list) != 2 || list[0].(map[string]any)["id"] != "devin/gemini-4-neon" {
		t.Fatalf("live_list = %v", devin["live_list"])
	}
	if value, present := vertex["live_list"]; !present || value != nil {
		t.Fatalf("every row of view=live carries live_list, null without one: %v", vertex)
	}
	provimuxValidate(t, "ModelListsResponse", raw)
}

func TestProvimuxModelListsNoSource(t *testing.T) {
	provimuxUseModelListSource(t, nil)
	provimuxGetModelLists(t, &Handler{cfg: &config.Config{}}, "", http.StatusServiceUnavailable)
}

func TestProvimuxModelListsEmpty(t *testing.T) {
	provimuxUseModelListSource(t, &provimuxFakeModelListSource{})
	body, raw := provimuxGetModelLists(t, &Handler{cfg: &config.Config{}}, "", http.StatusOK)
	if logins, ok := body["logins"].([]any); !ok || len(logins) != 0 {
		t.Fatalf("logins must be [] not null: %s", raw)
	}
	provimuxValidate(t, "ModelListsResponse", raw)
}

func TestProvimuxModelListsOptionOff(t *testing.T) {
	h, source := provimuxModelListFixture(t)
	h.cfg.Models.LiveLists = new(false)
	body, _ := provimuxGetModelLists(t, h, "", http.StatusOK)
	if body["options"].(map[string]any)["live_lists"] != false {
		t.Fatal("option off")
	}
	for _, asked := range source.asked {
		if strings.Contains(asked, ":true:") && strings.Index(asked, ":true:") == strings.Index(asked, ":") {
			t.Fatalf("the source must be told the option is off: %v", source.asked)
		}
	}
}

func TestProvimuxModelListsNeedsManagementKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := provimuxModelListFixture(t)
	h.cfg.RemoteManagement.SecretKey = ""
	router := gin.New()
	group := router.Group("/v8/management", h.Middleware())
	group.GET("/provimux/model-lists", h.ProvimuxModelLists)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v8/management/provimux/model-lists", nil))
	if recorder.Code == http.StatusOK {
		t.Fatalf("answered without a management key: %d", recorder.Code)
	}
}

// --- the OpenAPI contract ----------------------------------------------------------------------

func provimuxDesignDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".github", "provimux", "design", "pui13")
}

func provimuxOpenAPISchemas(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(provimuxDesignDir(t), "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if errYAML := yaml.Unmarshal(data, &doc); errYAML != nil {
		t.Fatalf("openapi.yaml: %v", errYAML)
	}
	return doc["components"].(map[string]any)["schemas"].(map[string]any)
}

// provimuxValidate checks a JSON document against a schema of openapi.yaml: types (with null),
// required keys, properties, items, enums, $ref and oneOf. It is the subset the contract uses.
func provimuxValidate(t *testing.T, schema string, document []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		t.Fatal(err)
	}
	schemas := provimuxOpenAPISchemas(t)
	if errs := provimuxCheck(schemas, schemas[schema], value, "$"); len(errs) > 0 {
		sort.Strings(errs)
		t.Fatalf("against %s:\n%s", schema, strings.Join(errs, "\n"))
	}
}

func provimuxCheck(schemas map[string]any, node any, value any, path string) []string {
	schema, _ := node.(map[string]any)
	if schema == nil {
		return nil
	}
	if ref, ok := schema["$ref"].(string); ok {
		return provimuxCheck(schemas, schemas[strings.TrimPrefix(ref, "#/components/schemas/")], value, path)
	}
	if options, ok := schema["oneOf"].([]any); ok {
		matched := 0
		for _, option := range options {
			if len(provimuxCheck(schemas, option, value, path)) == 0 {
				matched++
			}
		}
		if matched != 1 {
			return []string{fmt.Sprintf("%s: %d oneOf branches match", path, matched)}
		}
		return nil
	}
	var types []string
	switch declared := schema["type"].(type) {
	case string:
		types = []string{declared}
	case []any:
		for _, item := range declared {
			types = append(types, fmt.Sprint(item))
		}
	}
	if len(types) > 0 && !provimuxTypeMatches(types, value) {
		return []string{fmt.Sprintf("%s: %T is not %v", path, value, types)}
	}
	if enum, ok := schema["enum"].([]any); ok && value != nil {
		found := false
		for _, item := range enum {
			found = found || fmt.Sprint(item) == fmt.Sprint(value)
		}
		if !found {
			return []string{fmt.Sprintf("%s: %v not in %v", path, value, enum)}
		}
	}
	var errs []string
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range schema["required"].([]any) {
			if _, ok := typed[fmt.Sprint(key)]; !ok {
				errs = append(errs, fmt.Sprintf("%s: missing %v", path, key))
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for key, item := range typed {
			if property, ok := properties[key]; ok {
				errs = append(errs, provimuxCheck(schemas, property, item, path+"."+key)...)
			} else if properties != nil {
				errs = append(errs, fmt.Sprintf("%s: %s is not in the contract", path, key))
			}
		}
	case []any:
		for i, item := range typed {
			errs = append(errs, provimuxCheck(schemas, schema["items"], item, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return errs
}

func provimuxTypeMatches(types []string, value any) bool {
	for _, want := range types {
		switch value.(type) {
		case nil:
			if want == "null" {
				return true
			}
		case string:
			if want == "string" {
				return true
			}
		case bool:
			if want == "boolean" {
				return true
			}
		case float64:
			if want == "number" || (want == "integer" && value.(float64) == float64(int64(value.(float64)))) {
				return true
			}
		case map[string]any:
			if want == "object" {
				return true
			}
		case []any:
			if want == "array" {
				return true
			}
		}
	}
	return false
}

func TestProvimuxModelListsExamplesFollowTheContract(t *testing.T) {
	for _, name := range []string{"model-lists.json", "model-lists-login.json", "model-lists-live.json"} {
		data, err := os.ReadFile(filepath.Join(provimuxDesignDir(t), "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) { provimuxValidate(t, "ModelListsResponse", data) })
	}
	data, err := os.ReadFile(filepath.Join(provimuxDesignDir(t), "examples", "live-lists.json"))
	if err != nil {
		t.Fatal(err)
	}
	provimuxValidate(t, "LiveListsResponse", data)
}

func TestProvimuxModelListsValidatorHasTeeth(t *testing.T) {
	schemas := provimuxOpenAPISchemas(t)
	bad := map[string]any{"observed_at": "x", "options": map[string]any{"live_lists": "yes"}}
	if errs := provimuxCheck(schemas, schemas["ModelListsResponse"], bad, "$"); len(errs) < 2 {
		t.Fatalf("a broken document must fail: %v", errs)
	}
}

// Step P3: the published block reports providers.dev's held document, its last failure as a code, and
// "off" while models.published-lists is off.
func TestProvimuxModelListsPublishedBlock(t *testing.T) {
	h, _ := provimuxModelListFixture(t)
	SetProvimuxPublishedStateSource(func() ProvimuxPublishedState {
		return ProvimuxPublishedState{State: "held", DatasetVersion: "3463331602dfba12", GeneratedAt: provimuxFetchedAt,
			FetchedAt: provimuxFetchedAt.Add(time.Hour), FromCache: true,
			Failure: &ProvimuxLiveListFailure{At: provimuxFetchedAt.Add(2 * time.Hour), Category: "http_status", HTTPStatus: 503}}
	})
	t.Cleanup(func() { SetProvimuxPublishedStateSource(nil) })

	body, raw := provimuxGetModelLists(t, h, "", http.StatusOK)
	if body["published"].(map[string]any)["state"] != "off" || body["options"].(map[string]any)["published_lists"] != false {
		t.Fatalf("unset in a test binary means off: %s", raw)
	}
	h.cfg.Models.PublishedLists = new(true)
	body, raw = provimuxGetModelLists(t, h, "", http.StatusOK)
	published := body["published"].(map[string]any)
	failure, _ := published["last_failure"].(map[string]any)
	if body["options"].(map[string]any)["published_lists"] != true || published["state"] != "held" ||
		published["dataset_version"] != "3463331602dfba12" || published["generated_at"] != "2026-11-19T16:10:44Z" ||
		published["fetched_at"] != "2026-11-19T17:10:44Z" || published["from_cache"] != true ||
		failure == nil || failure["category"] != "http_status" || failure["http_status"].(float64) != 503 {
		t.Fatalf("published = %s", raw)
	}
	provimuxValidate(t, "ModelListsResponse", raw)

	SetProvimuxPublishedStateSource(func() ProvimuxPublishedState { return ProvimuxPublishedState{State: "none"} })
	body, raw = provimuxGetModelLists(t, h, "", http.StatusOK)
	if published = body["published"].(map[string]any); published["state"] != "none" || published["dataset_version"] != nil || published["last_failure"] != nil {
		t.Fatalf("none = %s", raw)
	}
	provimuxValidate(t, "ModelListsResponse", raw)
}
