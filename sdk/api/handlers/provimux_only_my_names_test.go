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
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// namesCountingExecutor stands in for a provider: it counts every call and answers.
type namesCountingExecutor struct {
	provider string
	calls    atomic.Int32
}

func (e *namesCountingExecutor) Identifier() string { return e.provider }

func (e *namesCountingExecutor) Execute(ctx context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	e.calls.Add(1)
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

// D5 (only-my-names.md section 9, decision 6): a plugin model router that sends a held-back name to a
// provider, and a forced provider, cannot reach it: account selection needs the account to have the
// name registered, so the request ends in upstream's "no auth available" before any executor runs.
func TestProvimuxOnlyMyNamesRouterAndForcedProviderCannotReachAHeldName(t *testing.T) {
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	rule := registry.NewProvimuxNamesRule(true, false)
	rule.AddName("names-d5-mine", registry.ProvimuxNameSetByAlias)
	registry.SetProvimuxNamesRule(rule)

	manager := coreauth.NewManager(nil, nil, nil)
	exec := &namesCountingExecutor{provider: "claude"}
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{ID: "names-d5-key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-d5"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	offered := []*registry.ModelInfo{{ID: "names-d5-mine", OwnedBy: "anthropic"}, {ID: "names-d5-held", OwnedBy: "anthropic"}}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", registry.ProvimuxOnlyMyNames(auth.ID, "", offered))
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	host := &handlerRouterOnlyTestHost{hasRouters: true, route: func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "claude"}, true
	}}
	routed := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	routed.SetModelRouterHost(host)
	plain := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	for _, tc := range []struct {
		name string
		run  func(model string) error
	}{
		{"plugin model router", func(model string) error {
			_, _, errMsg := routed.ExecuteWithAuthManager(contextWithHeaders(http.Header{}), "openai", model, []byte(`{"model":"`+model+`"}`), "")
			if errMsg != nil {
				return errMsg.Error
			}
			return nil
		}},
		{"forced provider", func(model string) error {
			_, errMsg := plain.ExecuteModel(contextWithHeaders(http.Header{}), ModelExecutionRequest{EntryProtocol: "openai", ExitProtocol: "openai",
				Model: model, ForcedProvider: "claude", Body: []byte(`{"model":"` + model + `"}`)})
			if errMsg != nil {
				return errMsg.Error
			}
			return nil
		}},
	} {
		before := exec.calls.Load()
		err := tc.run("names-d5-held")
		if err == nil || exec.calls.Load() != before {
			t.Fatalf("%s: a held-back name reached the provider (err %v, calls %d)", tc.name, err, exec.calls.Load()-before)
		}
		if !strings.Contains(err.Error(), "auth") {
			t.Fatalf("%s: refused with %v, want upstream's no auth available", tc.name, err)
		}
		if err := tc.run("names-d5-mine"); err != nil || exec.calls.Load() != before+1 {
			t.Fatalf("%s: his name: err %v, calls %d", tc.name, err, exec.calls.Load()-before)
		}
	}
	if !host.called {
		t.Fatal("the router was not asked")
	}
}
