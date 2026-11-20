package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const authProviderTestModel = "auth-provider-model"

// authProviderExecutor answers a Claude message for one provider, and fails with a retryable error for the auths in
// fail.
type authProviderExecutor struct {
	provider string
	fail     map[string]bool
	served   *[]string
}

type authProviderUnavailableError struct{}

func (authProviderUnavailableError) Error() string   { return "upstream unavailable" }
func (authProviderUnavailableError) StatusCode() int { return http.StatusServiceUnavailable }

func (e *authProviderExecutor) Identifier() string { return e.provider }

func (e *authProviderExecutor) Execute(ctx context.Context, a *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	*e.served = append(*e.served, a.ID)
	if e.fail[a.ID] {
		coreexecutor.MarkUpstreamAttempt(ctx)
		return coreexecutor.Response{}, authProviderUnavailableError{}
	}
	// Like a real executor's transport: the response comes from an upstream exchange.
	coreexecutor.MarkUpstreamAttempt(ctx)
	return coreexecutor.Response{Payload: []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"auth-provider-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)}, nil
}

func (e *authProviderExecutor) ExecuteStream(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}

func (e *authProviderExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *authProviderExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *authProviderExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// authProviderServer registers the credentials, all serving authProviderTestModel, with an executor per provider.
func authProviderServer(t *testing.T, credentials []*auth.Auth, fail map[string]bool) (*Server, *[]string) {
	t.Helper()
	server := newTestServer(t)
	served := &[]string{}
	registered := map[string]bool{}
	for _, credential := range credentials {
		if !registered[credential.Provider] {
			server.handlers.AuthManager.RegisterExecutor(&authProviderExecutor{provider: credential.Provider, fail: fail, served: served})
			registered[credential.Provider] = true
		}
		if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
			t.Fatalf("register %s: %v", credential.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: authProviderTestModel, OwnedBy: "anthropic"}})
		id := credential.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return server, served
}

func authProviderMessage(t *testing.T, server *Server) http.Header {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"auth-provider-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	return rr.Header()
}

// authProviderAccounts returns the accounts /v1/models?include=accounts lists for authProviderTestModel.
func authProviderAccounts(t *testing.T, server *Server) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models?include=accounts", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /v1/models?include=accounts = %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []struct {
			ID       string           `json:"id"`
			Accounts []map[string]any `json:"accounts"`
		} `json:"data"`
	}
	if errDecode := json.Unmarshal(rr.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode models: %v", errDecode)
	}
	for _, entry := range body.Data {
		if entry.ID == authProviderTestModel {
			return entry.Accounts
		}
	}
	t.Fatalf("%s not listed: %s", authProviderTestModel, rr.Body.String())
	return nil
}

func TestMessagesResponseNamesTheProviderAndHostOfTheCredentialThatServedIt(t *testing.T) {
	for _, test := range []struct {
		name       string
		credential *auth.Auth
		kind       string
		provider   string
		host       string
	}{
		{
			name:       "api key with a base URL",
			credential: &auth.Auth{ID: "auth-provider-compat", Provider: "openai-compatible-lab", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "sk-secret-test", "base_url": "https://user:secret-pass@API.Example.test:8443/acct-secret-id/v1?token=secret-token"}},
			kind:       "apikey", provider: "openai-compatible-lab", host: "api.example.test",
		},
		{
			name:       "api key without a base URL",
			credential: &auth.Auth{ID: "auth-provider-key", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "sk-secret-test"}},
			kind:       "apikey", provider: "claude",
		},
		{
			name:       "login",
			credential: &auth.Auth{ID: "auth-provider-login", Provider: "devin", Status: auth.StatusActive, Label: "someone@example.test", FileName: "devin-someone.json", Attributes: map[string]string{auth.AttributeAuthKind: "oauth", "base_url": "https://login.example.test/acct-secret-id"}},
			kind:       "oauth", provider: "devin",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, served := authProviderServer(t, []*auth.Auth{test.credential}, nil)
			headers := authProviderMessage(t, server)
			if len(*served) != 1 {
				t.Fatalf("served by %v", *served)
			}
			if got := headers.Get(internallogging.CPAAuthKindHeader); got != test.kind {
				t.Fatalf("%s = %q, want %q", internallogging.CPAAuthKindHeader, got, test.kind)
			}
			if got := headers.Get(internallogging.CPAAuthProviderHeader); got != test.provider {
				t.Fatalf("%s = %q, want %q", internallogging.CPAAuthProviderHeader, got, test.provider)
			}
			if _, present := headers[http.CanonicalHeaderKey(internallogging.CPAAuthHostHeader)]; present != (test.host != "") {
				t.Fatalf("%s present = %v, want %v (%v)", internallogging.CPAAuthHostHeader, present, test.host != "", headers)
			}
			if got := headers.Get(internallogging.CPAAuthHostHeader); got != test.host {
				t.Fatalf("%s = %q, want %q", internallogging.CPAAuthHostHeader, got, test.host)
			}
			exposed := headers.Get("Access-Control-Expose-Headers")
			for _, name := range []string{internallogging.CPAAuthProviderHeader, internallogging.CPAAuthHostHeader} {
				if !strings.Contains(exposed, name) {
					t.Fatalf("Access-Control-Expose-Headers = %q, want it to include %s", exposed, name)
				}
			}
			// Nothing that identifies the credential.
			for name, values := range headers {
				if !strings.HasPrefix(name, "X-Cpa-Auth-") {
					continue
				}
				for _, value := range values {
					for _, secret := range []string{"sk-secret-test", "secret-pass", "secret-token", "acct-secret-id", "8443", "someone@example.test", "devin-someone", test.credential.ID} {
						if strings.Contains(value, secret) {
							t.Fatalf("%s = %q exposes %q", name, value, secret)
						}
					}
				}
			}

			// The headers name the credential exactly as the model accounts list does.
			accounts := authProviderAccounts(t, server)
			if len(accounts) != 1 {
				t.Fatalf("accounts = %v, want one", accounts)
			}
			account := accounts[0]
			host, _ := account["host"].(string)
			if account["kind"] != headers.Get(internallogging.CPAAuthKindHeader) || account["provider"] != headers.Get(internallogging.CPAAuthProviderHeader) || host != headers.Get(internallogging.CPAAuthHostHeader) {
				t.Fatalf("account %v disagrees with headers kind=%q provider=%q host=%q", account, headers.Get(internallogging.CPAAuthKindHeader), headers.Get(internallogging.CPAAuthProviderHeader), headers.Get(internallogging.CPAAuthHostHeader))
			}
		})
	}
}

// A Claude login and a Devin login, both oauth, serve one model name: the kind is the same, the provider tells them
// apart.
func TestMessagesResponseTellsTwoLoginsOfOneModelApartByProvider(t *testing.T) {
	providers := map[string]bool{}
	for _, first := range []string{"claude", "devin"} {
		t.Run(first, func(t *testing.T) {
			priority := map[string]string{"claude": "-20", "devin": "-20"}
			priority[first] = "10"
			credentials := []*auth.Auth{
				{ID: "auth-provider-claude-login", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAuthKind: "oauth", "priority": priority["claude"]}},
				{ID: "auth-provider-devin-login", Provider: "devin", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAuthKind: "oauth", "priority": priority["devin"]}},
			}
			server, served := authProviderServer(t, credentials, nil)
			headers := authProviderMessage(t, server)
			if len(*served) != 1 || (*served)[0] != "auth-provider-"+first+"-login" {
				t.Fatalf("served by %v, want the %s login", *served, first)
			}
			if got := headers.Get(internallogging.CPAAuthKindHeader); got != "oauth" {
				t.Fatalf("%s = %q, want oauth", internallogging.CPAAuthKindHeader, got)
			}
			provider := headers.Get(internallogging.CPAAuthProviderHeader)
			if provider != first {
				t.Fatalf("%s = %q, want %q", internallogging.CPAAuthProviderHeader, provider, first)
			}
			if got := headers.Get(internallogging.CPAAuthHostHeader); got != "" {
				t.Fatalf("%s = %q for a login", internallogging.CPAAuthHostHeader, got)
			}
			listed := false
			for _, account := range authProviderAccounts(t, server) {
				listed = listed || (account["provider"] == provider && account["kind"] == "oauth")
			}
			if !listed {
				t.Fatalf("provider %q is not among the accounts of %s", provider, authProviderTestModel)
			}
			providers[provider] = true
		})
	}
	if len(providers) != 2 {
		t.Fatalf("providers named = %v, want two different ones", providers)
	}
}

// The first credential, an API key with a base URL, fails before anything is sent; a login of another provider
// answers. All three headers name the login: the key's host does not linger.
func TestMessagesResponseNamesTheCredentialARetryLandedOn(t *testing.T) {
	credentials := []*auth.Auth{
		{ID: "auth-provider-retry-key", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "sk-secret-test", "base_url": "https://api.example.test/v1", "priority": "10"}},
		{ID: "auth-provider-retry-login", Provider: "devin", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAuthKind: "oauth", "priority": "-10"}},
	}
	server, served := authProviderServer(t, credentials, map[string]bool{"auth-provider-retry-key": true})
	headers := authProviderMessage(t, server)
	if len(*served) != 2 || (*served)[0] != "auth-provider-retry-key" || (*served)[1] != "auth-provider-retry-login" {
		t.Fatalf("served by %v, want the key then the login", *served)
	}
	if got := headers.Get(internallogging.CPAAuthKindHeader); got != "oauth" {
		t.Fatalf("%s = %q, want oauth", internallogging.CPAAuthKindHeader, got)
	}
	if got := headers.Get(internallogging.CPAAuthProviderHeader); got != "devin" {
		t.Fatalf("%s = %q, want devin", internallogging.CPAAuthProviderHeader, got)
	}
	if got, present := headers[http.CanonicalHeaderKey(internallogging.CPAAuthHostHeader)]; present {
		t.Fatalf("%s = %q after a retry on a login", internallogging.CPAAuthHostHeader, got)
	}
}
