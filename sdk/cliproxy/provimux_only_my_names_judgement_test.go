package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Section 18, by behaviour (beside TestProvimuxOnlyMyNamesJudgementReadsNoRegistry, which checks the source):
// under one rule value, which this test checks never changes, registering and unregistering models unrelated to
// the names it asks about changes no judgement of the list gate, the door, the router check, alpha search or
// model-routes' filter. Each judgement is also held to the rule: listed, let through, asked about exactly when the
// rule serves the name. Where the judgement lets a request through, what follows is upstream's own path (the
// provider answers, or upstream's unknown-model error when no account offers the name), never another refusal.

// judgementState is one state of the registry: which unrelated clients are registered.
type judgementState struct {
	name      string
	unrelated []int
}

var judgementStates = []judgementState{
	{"none", nil}, {"one", []int{0}}, {"many", []int{0, 1, 2, 3, 4, 5}}, {"some removed", []int{1, 4}}, {"none again", nil}, {"others", []int{2, 3, 5}},
}

// judgementUnrelated registers the unrelated clients of state, each with its own models, and unregisters the
// others. Some of their models are served (kept), some not, one is offered by several clients; none is a name
// the test asks about.
func judgementUnrelated(t *testing.T, register func(clientID string, models []*registry.ModelInfo), state judgementState) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	for i := range 6 {
		clientID := fmt.Sprintf("judgement-unrelated-%d", i)
		if !slices.Contains(state.unrelated, i) {
			reg.UnregisterClient(clientID)
			continue
		}
		var models []*registry.ModelInfo
		for j := range 1 + i {
			models = append(models, &registry.ModelInfo{ID: fmt.Sprintf("unrelated-%d-%d", i, j), OwnedBy: "test", Created: int64(1000 + i*10 + j)})
		}
		models = append(models, &registry.ModelInfo{ID: fmt.Sprintf("unrelated-kept-%d", i), OwnedBy: "test", Name: fmt.Sprintf("models/unrelated-kept-%d", i)})
		if i >= 2 { // one model not served, offered by several clients at once
			models = append(models, &registry.ModelInfo{ID: "unrelated-shared", OwnedBy: "test"})
		}
		register(clientID, models)
		t.Cleanup(func() { reg.UnregisterClient(clientID) })
	}
}

func judgementListed(list []map[string]any, id string) bool {
	for _, model := range list {
		for _, key := range []string{"id", "name"} {
			if value, _ := model[key].(string); value == id || value == "models/"+id {
				return true
			}
		}
	}
	return false
}

// The list gate alone, over a registry that goes down to a single model and back up: each probe id is
// registered by its own client, and is listed, in every format, exactly when the rule serves it.
func TestProvimuxOnlyMyNamesListJudgementIgnoresRegistrations(t *testing.T) {
	namesReset(t)
	rule := onlymynames.NewRule(true, false)
	rule.AddName("judge-mine", onlymynames.SetByAlias)
	rule.AddKeep([]string{"judge-kept", "unrelated-kept-0", "unrelated-kept-2", "unrelated-kept-4"})
	rule.AddPrefix("judge-crew")
	registry.SetProvimuxNamesRule(rule)
	value := onlymynames.Current()
	reg := registry.GetGlobalRegistry()
	for _, id := range reg.GetAvailableModelInfos() {
		t.Fatalf("premise: an empty registry, found %q", id.ID)
	}
	probes := []string{"judge-mine", "judge-kept", "judge-held", "judge-crew/judge-mine", "judge-crew/ judge-mine", "judge-crew/judge-held", "judge-mine(high)"}
	register := func(clientID string, models []*registry.ModelInfo) { reg.RegisterClient(clientID, "openai", models) }
	first := map[string]string{}
	for _, alone := range append([]string{""}, probes...) {
		for _, state := range judgementStates {
			// One probe client alone, then every probe client, under each state of the unrelated clients.
			for _, probe := range probes {
				clientID := "judgement-probe-" + probe
				if alone == "" || alone == probe {
					reg.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: probe, OwnedBy: "test"}})
					t.Cleanup(func() { reg.UnregisterClient(clientID) })
				} else {
					reg.UnregisterClient(clientID)
				}
			}
			judgementUnrelated(t, register, state)
			if onlymynames.Current() != value {
				t.Fatal("the rule value changed")
			}
			openai, gemini, infos := reg.GetAvailableModels("openai"), reg.GetAvailableModels("gemini"), reg.GetAvailableModelInfos()
			var registered []string
			for _, info := range infos {
				registered = append(registered, info.ID)
			}
			for _, model := range append(openai, gemini...) {
				id, _ := model["id"].(string)
				if id == "" {
					id, _ = model["name"].(string)
				}
				if id = strings.TrimPrefix(id, "models/"); !rule.Serves(id) {
					t.Fatalf("alone %q, state %s: %q is listed and not served", alone, state.name, id)
				}
			}
			for _, probe := range probes {
				if alone != "" && alone != probe {
					continue
				}
				got := fmt.Sprintf("openai %v gemini %v infos %v", judgementListed(openai, probe), judgementListed(gemini, probe), slices.Contains(registered, probe))
				want := fmt.Sprintf("openai %v gemini %v infos %v", rule.Serves(probe), rule.Serves(probe), rule.Serves(probe))
				if got != want {
					t.Fatalf("alone %q, state %s: %q listed %s, the rule says %s", alone, state.name, probe, got, want)
				}
				if before, ok := first[probe]; ok && before != got {
					t.Fatalf("alone %q, state %s: %q listed %s, before %s", alone, state.name, probe, got, before)
				}
				first[probe] = got
			}
		}
	}
}

// judgementRouter is a plugin model router that records which names it was asked about, and decides nothing,
// or sends every request to target on the provider.
type judgementRouter struct {
	asked            []string
	provider, target string
}

func (r *judgementRouter) RouteModel(_ context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	r.asked = append(r.asked, req.RequestedModel)
	if r.target == "" {
		return pluginapi.ModelRouteResponse{}, false
	}
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: r.provider, TargetModel: r.target}, true
}

func (r *judgementRouter) HasModelRouters() bool { return true }

func judgementContext() context.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return context.WithValue(context.Background(), "gin", c)
}

// The door, the router check, alpha search and model-routes' filter, on the real server, while unrelated
// accounts come and go.
func TestProvimuxOnlyMyNamesJudgementIgnoresRegistrations(t *testing.T) {
	vendor := newNamesVendor(t)
	setup := strings.Replace(namesDoorConfig(vendor.URL, true), "enabled: true", "enabled: true\n    keep: [kept-nobody, unrelated-kept-0, unrelated-kept-2, unrelated-kept-4]", 1)
	d := reviewDoor(t, setup, vendor)
	reg := registry.GetGlobalRegistry()
	for _, id := range []string{"mine", "team-a/mine-t", "held-compat", "held-codex"} {
		deadline := time.Now().Add(10 * time.Second)
		for reg.GetModelCount(id) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("premise: %s is registered", id)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	value := onlymynames.Current()
	rule := value
	// His and not his; offered and offered by no account; a copy, a spaced non-copy, a suffix.
	probes := []string{"mine", "mine(high)", "pool", "team-a/mine-t", "mine-t", "kept-nobody", "held-compat", "held-codex", "team-a/ mine-t",
		"team-a/held-compat", "nobody-offers", "held-compat(high)"}
	offered := func(name string) bool { return reg.GetModelCount(onlymynames.Base(name)) > 0 }
	// Unrelated accounts, known to the auth manager, so model-routes sees them as it sees any account.
	register := func(clientID string, models []*registry.ModelInfo) {
		auth := &coreauth.Auth{ID: clientID, Provider: "openai-compatibility", Attributes: map[string]string{"api_key": "sk-" + clientID}}
		if _, err := d.service.coreManager.Register(coreauth.WithSkipPersist(t.Context()), auth); err != nil {
			t.Fatal(err)
		}
		reg.RegisterClient(clientID, "openai-compatibility", models)
	}
	plain := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, d.service.coreManager)
	asking := &judgementRouter{}
	routed := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, d.service.coreManager)
	routed.SetModelRouterHost(asking)

	first := map[string]string{}
	for _, state := range judgementStates {
		judgementUnrelated(t, register, state)
		if onlymynames.Current() != value {
			t.Fatal("the rule value changed")
		}
		// model-routes: every id it shows is served, and each probe is shown exactly when it is served and offered.
		routes := reviewManagement(d.namesHarness, "GET", "provimux/model-routes", "").Body.String()
		shown := map[string]bool{}
		for _, id := range ojson.Get(routes, "models.#.id").Array() {
			shown[id.String()] = true
			if !rule.Serves(id.String()) {
				t.Fatalf("state %s: model-routes shows %q", state.name, id.String())
			}
		}
		for _, probe := range probes {
			refused := rule.Refuses(probe)
			// The door, on the registry path.
			body, _, errMsg := plain.ExecuteWithAuthManager(judgementContext(), "openai", probe, []byte(fmt.Sprintf(`{"model":%q}`, probe)), "")
			door := "passed"
			switch {
			case errMsg != nil && strings.Contains(errMsg.Error.Error(), namesNotServed):
				door = "refused"
			case errMsg != nil && strings.Contains(errMsg.Error.Error(), "unknown provider for model") && !offered(probe):
				door = "passed, no account"
			case errMsg != nil:
				t.Fatalf("state %s: %q: %d %s", state.name, probe, errMsg.StatusCode, errMsg.Error)
			case len(body) == 0:
				t.Fatalf("state %s: %q: an empty answer", state.name, probe)
			}
			if (door == "refused") != refused {
				t.Fatalf("state %s: the door %s %q, the rule refuses it: %v", state.name, door, probe, refused)
			}
			// The router check: a router is asked about a name exactly when it is served; the door then judges the
			// same name.
			asking.asked = nil
			_, _, errRouted := routed.ExecuteWithAuthManager(judgementContext(), "openai", probe, []byte(fmt.Sprintf(`{"model":%q}`, probe)), "")
			router := fmt.Sprintf("asked %v refused %v", len(asking.asked) > 0, errRouted != nil && strings.Contains(errRouted.Error.Error(), namesNotServed))
			if want := fmt.Sprintf("asked %v refused %v", !refused, refused); router != want {
				t.Fatalf("state %s: router check for %q: %s, want %s", state.name, probe, router, want)
			}
			// A router's target is judged under the same value: his name, sent to the probe.
			retarget := &judgementRouter{provider: "openai-compatibility", target: probe}
			retargeted := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, d.service.coreManager)
			retargeted.SetModelRouterHost(retarget)
			_, _, errTarget := retargeted.ExecuteWithAuthManager(judgementContext(), "openai", "mine", []byte(`{"model":"mine"}`), "")
			target := errTarget != nil && strings.Contains(errTarget.Error.Error(), namesNotServed)
			if target != refused || len(retarget.asked) != 1 {
				t.Fatalf("state %s: a router's target %q refused %v (asked %v), the rule refuses it: %v", state.name, probe, target, retarget.asked, refused)
			}
			// Alpha search: refused with the setting's message exactly when the rule refuses the name.
			status, answer := d.do("POST", "/v1/alpha/search", nil, fmt.Sprintf(`{"model":%q,"id":"s"}`, probe))
			alpha := strings.Contains(answer, namesNotServed)
			if alpha != refused || alpha && status != http.StatusBadRequest {
				t.Fatalf("state %s: alpha search %q: %d %s, the rule refuses it: %v", state.name, probe, status, answer, refused)
			}
			// model-routes' filter.
			if shown[probe] != (!refused && reg.GetModelCount(probe) > 0) {
				t.Fatalf("state %s: model-routes shows %q: %v (refused %v)", state.name, probe, shown[probe], refused)
			}
			got := fmt.Sprintf("door %s, %s, target refused %v, alpha %d %v, model-routes %v", door, router, target, status, alpha, shown[probe])
			if before, ok := first[probe]; ok && before != got {
				t.Fatalf("state %s: %q judged %s, before %s", state.name, probe, got, before)
			}
			first[probe] = got
		}
	}
	for _, probe := range probes {
		t.Logf("%-20q %s", probe, first[probe])
	}
}
