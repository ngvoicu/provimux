package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkhandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// nestedCallHost is a plugin that makes its own model call through the host, with another credential, from its
// response interceptor (a plain answer) or, for a stream, from its after-auth request interceptor. A stream that passes
// through a stream-chunk interceptor names nobody (DESIGN.md 12, after the third review), so for a stream the plugin has
// none in the stream's path.
type nestedCallHost struct {
	h      *sdkhandlers.BaseAPIHandler
	authID string
	stream bool
	called atomic.Int32
	nested atomic.Value
}

func (p *nestedCallHost) call(ctx context.Context) {
	if p.called.Add(1) != 1 {
		return
	}
	resp, errMsg := p.h.ExecuteModel(ctx, sdkhandlers.ModelExecutionRequest{EntryProtocol: "claude", Model: answeredTestModel, AuthID: p.authID,
		Body: []byte(fmt.Sprintf(`{"model":%q,"max_tokens":8,"messages":[{"role":"user","content":"nested"}]}`, answeredTestModel))})
	if errMsg != nil {
		p.nested.Store("ERR " + errMsg.Error.Error())
		return
	}
	p.nested.Store(string(resp.Body))
}

func (p *nestedCallHost) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func (p *nestedCallHost) InterceptRequestAfterAuth(ctx context.Context, _ pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if p.stream {
		p.call(ctx)
	}
	return pluginapi.RequestInterceptResponse{}
}

func (p *nestedCallHost) HasStreamInterceptors() bool { return false }

func (p *nestedCallHost) InterceptResponse(ctx context.Context, _ pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	if !p.stream {
		p.call(ctx)
	}
	return pluginapi.ResponseInterceptResponse{}
}

func (p *nestedCallHost) InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{}
}

// A plugin's own model call answers the plugin, not the client: the response names the credential whose body the
// client gets (the hosted key), never the one the plugin's call went out with (a Devin login).
func TestNestedPluginExecutionDoesNotRenameTheResponse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			hostedKey, devinLogin := answeredCredentials()
			cfg := &proxyconfig.Config{}
			cfg.APIKeys = []string{"test-key"}
			server := newTestServerWithConfig(t, cfg)
			attempts := []string{}
			mu := &sync.Mutex{}
			for _, credential := range []*auth.Auth{hostedKey, devinLogin} {
				server.handlers.AuthManager.RegisterExecutor(&answeredExecutor{provider: credential.Provider, attempts: &attempts, mu: mu})
				if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: answeredTestModel, OwnedBy: "anthropic"}})
				id := credential.ID
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			host := &nestedCallHost{h: server.handlers, authID: devinLogin.ID, stream: stream}
			server.handlers.SetPluginHost(host)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"max_tokens":8,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, answeredTestModel, stream)))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			server.engine.ServeHTTP(recorder, req)

			nested, _ := host.nested.Load().(string)
			wantCalls := "[answered-hosted-key answered-devin-login]"
			if stream { // the after-auth request interceptor runs inside the outer attempt, before its exchange
				wantCalls = "[answered-devin-login answered-hosted-key]"
			}
			mu.Lock()
			calls := fmt.Sprint(attempts)
			mu.Unlock()
			if recorder.Code != http.StatusOK || calls != wantCalls ||
				!strings.Contains(recorder.Body.String(), "served-by-answered-hosted-key") || !strings.Contains(nested, "served-by-answered-devin-login") {
				t.Fatalf("fixture: status=%d attempts=%s outer=%q nested=%q", recorder.Code, calls, recorder.Body.String(), nested)
			}
			want := map[string]string{internallogging.CPAAuthKindHeader: "apikey", internallogging.CPAAuthProviderHeader: "claude", internallogging.CPAAuthHostHeader: "openrouter.example.test"}
			for name, value := range want {
				if got := recorder.Header().Get(name); got != value {
					t.Fatalf("%s = %q, want %q: the body is the hosted key's", name, got, value)
				}
			}
		})
	}
}
