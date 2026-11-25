package handlers

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
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
	rule := onlymynames.NewRule(enabled, false)
	rule.AddName("names-d5-mine", onlymynames.SetByAlias)
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
	// A name no account registers is refused like any other, with the setting's message, and counted: the door
	// judges the rule and the name, and reads no registration (section 18).
	if body, status := execute(plain)("nobody-offers-this"); status != http.StatusBadRequest || !strings.Contains(body, "model nobody-offers-this "+namesNotServed) {
		t.Fatalf("unknown: %d %s", status, body)
	}
	counted := map[string]int{}
	for _, name := range onlymynames.RefusedNow().Names {
		counted[name.Name] = name.Requests
	}
	if counted["names-d5-held"] < 6 || counted["nobody-offers-this"] != 1 || len(counted) != 2 {
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
	rule := onlymynames.NewRule(true, false)
	rule.AddName("claude-opus-5-5", onlymynames.SetByKeep)
	registry.SetProvimuxNamesRule(rule)
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	b.ReportAllocs()
	for b.Loop() {
		model := "claude-opus-5-5"
		_ = h.provimuxDoor(&model, model, modelRouteDecision{})
	}
}

// F (section 17, the third review's note 6): auto is reserved. A literal auto on keep is ignored, so with none
// of his names available a request for auto is refused and no router is asked, keep or no keep.
func TestProvimuxOnlyMyNamesAutoIsNeverKept(t *testing.T) {
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	manager := coreauth.NewManager(nil, nil, nil)
	executorHost := &handlerDirectExecutorRouteHost{}
	executorHost.hasRouters = true
	executorHost.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Target: "names-plugin"}, true
	}
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h.SetModelRouterHost(executorHost)
	for _, keep := range [][]string{nil, {"auto"}} {
		rule := onlymynames.NewRule(true, false)
		rule.AddKeep(keep)
		registry.SetProvimuxNamesRule(rule)
		executorHost.called, executorHost.lastPluginID = false, ""
		_, _, errMsg := h.ExecuteWithAuthManager(contextWithHeaders(http.Header{}), "openai", "auto", []byte(`{"model":"auto"}`), "")
		if errMsg == nil || errMsg.StatusCode != http.StatusBadRequest || executorHost.called || executorHost.lastPluginID != "" {
			t.Fatalf("keep %v: auto: %v, router asked %v, plugin %q", keep, errMsg, executorHost.called, executorHost.lastPluginID)
		}
	}
}

// 2 of section 18 (the fourth review's finding 12): one request, one rule value. Rule A owns only the submitted
// name, rule B only the router's target. A commit of B between the door's judgement of the submitted name and of
// the target does not let the request through: both are judged under A, the value the door loaded first. The
// controls: A alone refuses (the target), B alone refuses (the name), a rule owning both serves.
func TestProvimuxOnlyMyNamesOneRuleValuePerRequest(t *testing.T) {
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	manager := coreauth.NewManager(nil, nil, nil)
	exec := &namesCountingExecutor{provider: "claude"}
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{ID: "names-one-rule-key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-one"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "names-submitted", OwnedBy: "anthropic"}, {ID: "names-target", OwnedBy: "anthropic"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	host := &handlerRouterOnlyTestHost{hasRouters: true, route: func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "claude", TargetModel: "names-target"}, true
	}}
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h.SetModelRouterHost(host)
	ruleOf := func(names ...string) *onlymynames.Rule {
		rule := onlymynames.NewRule(true, false)
		rule.AddKeep(names)
		return rule
	}
	run := func() int {
		before := exec.calls.Load()
		_, _, errMsg := h.ExecuteWithAuthManager(contextWithHeaders(http.Header{}), "openai", "names-submitted", []byte(`{"model":"names-submitted"}`), "")
		if errMsg != nil {
			if exec.calls.Load() != before {
				t.Fatal("refused after reaching the provider")
			}
			return errMsg.StatusCode
		}
		return http.StatusOK
	}
	for _, control := range []struct {
		names []string
		want  int
	}{{[]string{"names-submitted"}, 400}, {[]string{"names-target"}, 400}, {[]string{"names-submitted", "names-target"}, 200}} {
		registry.SetProvimuxNamesRule(ruleOf(control.names...))
		if got := run(); got != control.want {
			t.Fatalf("control %v: %d", control.names, got)
		}
	}
	previous := provimuxDoorBetween
	t.Cleanup(func() { provimuxDoorBetween = previous })
	committed := 0
	provimuxDoorBetween = func() {
		committed++
		registry.SetProvimuxNamesRule(ruleOf("names-target")) // B, between the two judgements
	}
	registry.SetProvimuxNamesRule(ruleOf("names-submitted")) // A
	before := exec.calls.Load()
	if got := run(); got != http.StatusBadRequest || committed != 1 || exec.calls.Load() != before {
		t.Fatalf("A then B between the judgements: %d (commits %d, calls %d)", got, committed, exec.calls.Load()-before)
	}
	if onlymynames.Current().Has("names-submitted") {
		t.Fatal("premise: B is in force after the request")
	}
}

// The fourth review's router alternation (finding 12), uninstrumented: rules owning only the submitted name and
// only the router's target alternate as fast as they can while requests run. Neither rule owns both, so no request
// may reach the provider.
func TestProvimuxOnlyMyNamesRouterUnderAlternatingRules(t *testing.T) {
	if os.Getenv("PROVIMUX_G3_G2_ONE_VALUE") == "" {
		// Open (only-my-names.md 18.2): G3 and the door load the rule separately; carrying one value between
		// them needs one more inserted upstream line, or the fallback. Run with PROVIMUX_G3_G2_ONE_VALUE=1.
		t.Skip("open decision: one rule value across the router check and the door")
	}
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	manager := coreauth.NewManager(nil, nil, nil)
	exec := &namesCountingExecutor{provider: "claude"}
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{ID: "names-alternating-key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-alt"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "names-submitted", OwnedBy: "anthropic"}, {ID: "names-target", OwnedBy: "anthropic"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h.SetModelRouterHost(&handlerRouterOnlyTestHost{hasRouters: true, route: func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "claude", TargetModel: "names-target"}, true
	}})
	a, b := onlymynames.NewRule(true, false), onlymynames.NewRule(true, false)
	a.AddKeep([]string{"names-submitted"})
	b.AddKeep([]string{"names-target"})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				registry.SetProvimuxNamesRule(a)
			} else {
				registry.SetProvimuxNamesRule(b)
			}
		}
	}()
	served := 0
	for range 2000 {
		if _, _, errMsg := h.ExecuteWithAuthManager(contextWithHeaders(http.Header{}), "openai", "names-submitted", []byte(`{"model":"names-submitted"}`), ""); errMsg == nil {
			served++
		}
	}
	close(stop)
	<-done
	if served != 0 || exec.calls.Load() != 0 {
		t.Fatalf("%d requests served, %d provider calls, under rules that each refuse them", served, exec.calls.Load())
	}
}
