package handlers

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// namesCountingExecutor stands in for a provider: it counts every call and answers.
type namesCountingExecutor struct {
	provider string
	calls    atomic.Int32
	model    atomic.Value // the model of the last call
	alias    atomic.Value // the requested-model alias the usage record will carry
}

func (e *namesCountingExecutor) Identifier() string { return e.provider }

func (e *namesCountingExecutor) Execute(ctx context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	e.calls.Add(1)
	e.model.Store(req.Model)
	e.alias.Store(coreusage.RequestedModelAliasFromContext(ctx))
	return coreexecutor.Response{Payload: []byte(`{"served":"` + req.Model + `"}`)}, nil
}

func (e *namesCountingExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *namesCountingExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, &coreauth.Error{Code: "unsupported", Message: "no stream", HTTPStatus: http.StatusBadRequest}
}

func (e *namesCountingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *namesCountingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	return nil, nil
}

const namesNotServed = "is not served: this Provimux serves only the names its owner set (models.only-my-names)"

func namesD5Rule(t *testing.T, enabled bool) {
	t.Helper()
	rule := registry.NewProvimuxNamesRule(enabled, false)
	rule.AddName("names-d5-mine", registry.ProvimuxNameSetByAlias)
	registry.SetProvimuxNamesRule(rule)
}

// K2 and D5 at the handlers (only-my-names.md 16.3, G2 and G3): a forced provider, a router's provider
// decision (for a name that is not his, and with a target that is not his), and a router's decision for its
// own executor cannot reach a name that is not his. Each is refused at the door with the setting's message
// (decision 6 of section 11 is gone), no executor runs, and for a name that is not his no router is asked.
func TestProvimuxOnlyMyNamesDoorRefusesEveryExecutionPath(t *testing.T) {
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	namesD5Rule(t, true)

	manager := coreauth.NewManager(nil, nil, nil)
	exec := &namesCountingExecutor{provider: "claude"}
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{ID: "names-d5-key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-d5"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "names-d5-mine", OwnedBy: "anthropic"}, {ID: "names-d5-held", OwnedBy: "anthropic"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	providerHost := &handlerRouterOnlyTestHost{hasRouters: true, route: func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "claude"}, true
	}}
	retargetHost := &handlerRouterOnlyTestHost{hasRouters: true, route: func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "claude", TargetModel: "names-d5-held"}, true
	}}
	executorHost := &handlerDirectExecutorRouteHost{}
	executorHost.hasRouters = true
	executorHost.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Target: "names-plugin"}, true
	}
	withRouter := func(host PluginModelRouterHost) *BaseAPIHandler {
		h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
		h.SetModelRouterHost(host)
		return h
	}
	routed, retargeted, toExecutor := withRouter(providerHost), withRouter(retargetHost), withRouter(executorHost)
	plain := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	execute := func(h *BaseAPIHandler) func(model string) (string, int) {
		return func(model string) (string, int) {
			body, _, errMsg := h.ExecuteWithAuthManager(contextWithHeaders(http.Header{}), "openai", model, []byte(`{"model":"`+model+`"}`), "")
			if errMsg != nil {
				return errMsg.Error.Error(), errMsg.StatusCode
			}
			return string(body), http.StatusOK
		}
	}
	for _, tc := range []struct {
		name string
		run  func(model string) (string, int)
		held string
	}{
		{"registry path", execute(plain), "names-d5-held"},
		{"registry path, thinking suffix", execute(plain), "names-d5-held(high)"},
		{"router's provider decision", execute(routed), "names-d5-held"},
		{"router's executor decision", execute(toExecutor), "names-d5-held"},
		{"forced provider", func(model string) (string, int) {
			resp, errMsg := plain.ExecuteModel(contextWithHeaders(http.Header{}), ModelExecutionRequest{EntryProtocol: "openai", ExitProtocol: "openai",
				Model: model, ForcedProvider: "claude", Body: []byte(`{"model":"` + model + `"}`)})
			if errMsg != nil {
				return errMsg.Error.Error(), errMsg.StatusCode
			}
			return string(resp.Body), http.StatusOK
		}, "names-d5-held"},
		{"count tokens", func(model string) (string, int) {
			_, _, errMsg := plain.ExecuteCountWithAuthManager(contextWithHeaders(http.Header{}), "openai", model, []byte(`{"model":"`+model+`"}`), "")
			if errMsg != nil {
				return errMsg.Error.Error(), errMsg.StatusCode
			}
			return "", http.StatusOK
		}, "names-d5-held"},
	} {
		providerHost.called, executorHost.called, executorHost.lastPluginID = false, false, ""
		before := exec.calls.Load()
		body, status := tc.run(tc.held)
		if status != http.StatusBadRequest || exec.calls.Load() != before || executorHost.lastPluginID != "" {
			t.Fatalf("%s: a name that is not his: %d %s (calls %d, plugin %q)", tc.name, status, body, exec.calls.Load()-before, executorHost.lastPluginID)
		}
		if !strings.Contains(body, "model names-d5-held "+namesNotServed) || !strings.Contains(body, `"code":"model_not_found"`) || !strings.Contains(body, `"param":"model"`) {
			t.Fatalf("%s: refused with %s", tc.name, body)
		}
		if providerHost.called || executorHost.called {
			t.Fatalf("%s: a router was asked about a name that is not his", tc.name)
		}
		if body, status := tc.run("names-d5-mine"); status != http.StatusOK || (exec.calls.Load() != before+1 && executorHost.lastPluginID == "") {
			t.Fatalf("%s: his name: %d %s", tc.name, status, body)
		}
	}
	providerHost.called = false
	if _, status := execute(routed)("names-d5-mine"); status != http.StatusOK || !providerHost.called {
		t.Fatal("the router was not asked about his name")
	}
	// A router's decision whose target is not his, for a name that is.
	before := exec.calls.Load()
	if body, status := execute(retargeted)("names-d5-mine"); status != http.StatusBadRequest || !strings.Contains(body, "model names-d5-held "+namesNotServed) || exec.calls.Load() != before {
		t.Fatalf("a router's target that is not his: %d %s", status, body)
	}
	// A name no account registers keeps upstream's words and is not counted.
	if body, status := execute(plain)("nobody-offers-this"); status != http.StatusBadRequest || !strings.Contains(body, "unknown provider for model nobody-offers-this") {
		t.Fatalf("unknown: %d %s", status, body)
	}
	counted := map[string]int{}
	for _, name := range registry.ProvimuxRefusedNow().Names {
		counted[name.Name] = name.Requests
	}
	if counted["names-d5-held"] < 6 || counted["nobody-offers-this"] != 0 || len(counted) != 1 {
		t.Fatalf("counted %v", counted)
	}

	// auto becomes the first available of his names.
	if body, status := execute(plain)("auto"); status != http.StatusOK || exec.model.Load() != "names-d5-mine" {
		t.Fatalf("auto: %d %s, executed %v", status, body, exec.model.Load())
	}

	// E: the usage record of a request carries the name the client sent (Record.Alias comes from this
	// context), suffix included; Savings keeps it, without the suffix, apart from its rows.
	for _, model := range []string{"names-d5-mine", "names-d5-mine(high)"} {
		if _, _, errMsg := plain.ExecuteWithAuthManager(contextWithHeaders(http.Header{}), "openai", model, []byte(`{"model":"`+model+`"}`), ""); errMsg != nil {
			t.Fatalf("%s: %v", model, errMsg.Error)
		}
		if got, _ := exec.alias.Load().(string); got != model {
			t.Fatalf("%s: the usage alias is %q", model, got)
		}
	}

	// The control: with the switch off every path reaches the provider or the plugin as before.
	namesD5Rule(t, false)
	for _, h := range []*BaseAPIHandler{plain, routed} {
		before := exec.calls.Load()
		if body, status := execute(h)("names-d5-held"); status != http.StatusOK || exec.calls.Load() != before+1 {
			t.Fatalf("off: %d %s", status, body)
		}
	}
	executorHost.lastPluginID = ""
	if body, status := execute(toExecutor)("names-d5-held"); status != http.StatusOK || executorHost.lastPluginID != "names-plugin" {
		t.Fatalf("off, router to its executor: %d %s", status, body)
	}
}

// O2 and the bound of 16.7: the door allocates nothing with the switch off, and nothing for one of his names
// with it on; provimuxNoRouter neither.
func TestProvimuxOnlyMyNamesDoorAllocatesNothing(t *testing.T) {
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	measure := func(name string) float64 {
		return testing.AllocsPerRun(200, func() {
			model := name
			if errMsg := h.provimuxDoor(&model, name, modelRouteDecision{}); errMsg != nil {
				t.Fatalf("%s refused", name)
			}
			provimuxNoRouter(name)
		})
	}
	if allocs := measure("anything-at-all"); allocs != 0 {
		t.Fatalf("off: %v allocations", allocs)
	}
	namesD5Rule(t, true)
	if allocs := measure("names-d5-mine"); allocs != 0 {
		t.Fatalf("on, his name: %v allocations", allocs)
	}
	if allocs := measure("names-d5-mine(high)"); allocs != 0 {
		t.Fatalf("on, his name with a suffix: %v allocations", allocs)
	}
}

func BenchmarkProvimuxOnlyMyNamesDoorOff(b *testing.B) {
	registry.ResetProvimuxNames()
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	b.ReportAllocs()
	for b.Loop() {
		model := "claude-opus-5-5"
		_ = h.provimuxDoor(&model, model, modelRouteDecision{})
	}
}

func BenchmarkProvimuxOnlyMyNamesDoorOnHisName(b *testing.B) {
	registry.ResetProvimuxNames()
	b.Cleanup(registry.ResetProvimuxNames)
	rule := registry.NewProvimuxNamesRule(true, false)
	rule.AddName("claude-opus-5-5", registry.ProvimuxNameSetByKeep)
	registry.SetProvimuxNamesRule(rule)
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	b.ReportAllocs()
	for b.Loop() {
		model := "claude-opus-5-5"
		_ = h.provimuxDoor(&model, model, modelRouteDecision{})
	}
}
