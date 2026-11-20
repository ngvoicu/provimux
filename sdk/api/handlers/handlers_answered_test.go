package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const answeredOfferModel = "answered-offer-model"

type answeredOfferBusy struct{}

func (answeredOfferBusy) Error() string   { return "fixture upstream unavailable" }
func (answeredOfferBusy) StatusCode() int { return http.StatusServiceUnavailable }

// answeredOfferExecutor stands for an upstream: every attempt marks its exchange, as an executor's transport does.
// A credential listed in fail answers with an error; one listed in discard opens a stream that fails before its
// first deliverable payload.
type answeredOfferExecutor struct {
	provider string
	fail     map[string]bool
	discard  map[string]bool
}

func (e *answeredOfferExecutor) Identifier() string { return e.provider }

func (e *answeredOfferExecutor) Execute(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	if e.fail[auth.ID] {
		return coreexecutor.Response{}, answeredOfferBusy{}
	}
	return coreexecutor.Response{Payload: []byte("served-by-" + auth.ID)}, nil
}

func (e *answeredOfferExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *answeredOfferExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	if e.fail[auth.ID] {
		return nil, answeredOfferBusy{}
	}
	chunks := make(chan coreexecutor.StreamChunk, 2)
	if e.discard[auth.ID] {
		// Part of a Responses event, then the upstream fails: the execution layer holds the part and retries.
		chunks <- coreexecutor.StreamChunk{Payload: []byte("event: response.completed\ndata: {\"type\":\"response.completed\",")}
		chunks <- coreexecutor.StreamChunk{Err: &coreauth.Error{Code: "unavailable", Message: "fixture stream interrupted", HTTPStatus: http.StatusServiceUnavailable}}
	} else {
		chunks <- coreexecutor.StreamChunk{Payload: []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"served-by-" + auth.ID + "\"}}\n\n")}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *answeredOfferExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *answeredOfferExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("not used")
}

var (
	answeredOfferKey   = logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "openrouter.example.test"}
	answeredOfferLogin = logging.AuthStatement{Kind: "oauth", Provider: "devin"}
	answeredOfferStale = logging.AuthStatement{Kind: "apikey", Provider: "earlier", Host: "earlier.example.test"}
)

// newAnsweredOfferHandler registers a hosted key ("key", tried first) and a login ("login") serving one model.
func newAnsweredOfferHandler(t *testing.T, cfg *sdkconfig.SDKConfig, fail, discard map[string]bool) *BaseAPIHandler {
	t.Helper()
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	credentials := []*coreauth.Auth{
		{ID: "key", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "made-up", "base_url": "https://openrouter.example.test/v1", "priority": "10"}},
		{ID: "login", Provider: "devin", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "priority": "0"}},
	}
	for _, credential := range credentials {
		manager.RegisterExecutor(&answeredOfferExecutor{provider: credential.Provider, fail: fail, discard: discard})
		if _, err := manager.Register(context.Background(), credential); err != nil {
			t.Fatalf("register %s: %v", credential.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: answeredOfferModel}})
		id := credential.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return NewBaseAPIHandlers(cfg, manager)
}

// answeredOfferRequest is an HTTP request's context, with an earlier statement already offered on it.
func answeredOfferRequest(t *testing.T) (*gin.Context, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	logging.OfferGinCPAAuthStatement(c, answeredOfferStale)
	return c, context.WithValue(context.Background(), "gin", c)
}

// offered reads what the request holds on offer, as a route does: by confirming it before a write.
func offered(c *gin.Context) logging.AuthStatement {
	ConfirmAnswer(c)
	return logging.GetGinCPAAuthStatement(c)
}

// The execution layer offers, with each result it returns, the statement of the credential whose upstream produced
// that result, and nothing else: none with an error, the retry's with a stream whose first attempt was discarded,
// none from a plugin executor (no credential of this proxy answered), and a plugin's own model call leaves the
// request's offer as it was.
func TestExecutionOffersTheStatementOfTheResultItReturns(t *testing.T) {
	payload := []byte(fmt.Sprintf(`{"model":%q}`, answeredOfferModel))
	plain := func(h *BaseAPIHandler, ctx context.Context) (string, bool) {
		body, _, errMsg := h.ExecuteWithAuthManager(ctx, "claude", answeredOfferModel, payload, "")
		return string(body), errMsg == nil
	}
	count := func(h *BaseAPIHandler, ctx context.Context) (string, bool) {
		body, _, errMsg := h.ExecuteCountWithAuthManager(ctx, "claude", answeredOfferModel, payload, "")
		return string(body), errMsg == nil
	}
	stream := func(h *BaseAPIHandler, ctx context.Context) (string, bool) {
		data, _, errs := h.ExecuteStreamWithAuthManager(ctx, "openai-response", answeredOfferModel, payload, "")
		var body []byte
		if data != nil {
			for chunk := range data {
				body = append(body, chunk...)
			}
		}
		ok := true
		for errMsg := range errs {
			if errMsg != nil {
				ok = false
			}
		}
		return string(body), ok
	}

	for _, test := range []struct {
		name     string
		execute  func(*BaseAPIHandler, context.Context) (string, bool)
		pin      string
		fail     map[string]bool
		discard  map[string]bool
		retries  int
		wantBody string
		want     logging.AuthStatement
	}{
		{name: "plain success", execute: plain, pin: "key", wantBody: "served-by-key", want: answeredOfferKey},
		{name: "plain error", execute: plain, pin: "key", fail: map[string]bool{"key": true}},
		{name: "plain after a failed first credential", execute: plain, fail: map[string]bool{"key": true}, wantBody: "served-by-login", want: answeredOfferLogin},
		{name: "count success", execute: count, pin: "login", wantBody: "served-by-login", want: answeredOfferLogin},
		{name: "count error", execute: count, pin: "login", fail: map[string]bool{"login": true}},
		{name: "stream success", execute: stream, pin: "key", wantBody: "served-by-key", want: answeredOfferKey},
		{name: "stream error", execute: stream, pin: "key", fail: map[string]bool{"key": true}},
		{name: "stream whose first attempt is discarded", execute: stream, discard: map[string]bool{"key": true}, retries: 1, wantBody: "served-by-login", want: answeredOfferLogin},
		{name: "stream discarded without a retry", execute: stream, discard: map[string]bool{"key": true}},
		{name: "stream discarded, its retry fails", execute: stream, discard: map[string]bool{"key": true}, fail: map[string]bool{"login": true}, retries: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAnsweredOfferHandler(t, &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: test.retries}}, test.fail, test.discard)
			c, ctx := answeredOfferRequest(t)
			if test.pin != "" {
				ctx = WithPinnedAuthID(ctx, test.pin)
			}
			body, ok := test.execute(h, ctx)
			if ok != (test.wantBody != "") || !strings.Contains(body, test.wantBody) || (test.wantBody == "" && body != "") {
				t.Fatalf("result = %q (success %v), want %q", body, ok, test.wantBody)
			}
			if got := offered(c); got != test.want {
				t.Fatalf("offered %+v, want %+v", got, test.want)
			}
		})
	}

	t.Run("websocket turn", func(t *testing.T) {
		h := newAnsweredOfferHandler(t, &sdkconfig.SDKConfig{}, nil, nil)
		var turn []logging.AuthStatement
		ctx := WithAnsweredAuthCallback(WithPinnedAuthID(context.Background(), "key"), func(kind, provider, host string) {
			turn = append(turn, logging.AuthStatement{Kind: kind, Provider: provider, Host: host})
		})
		if body, ok := stream(h, ctx); !ok || !strings.Contains(body, "served-by-key") {
			t.Fatalf("result = %q (success %v)", body, ok)
		}
		if len(turn) != 1 || turn[0] != answeredOfferKey {
			t.Fatalf("turn received %+v, want one %+v", turn, answeredOfferKey)
		}
	})

	t.Run("a plugin's own model call", func(t *testing.T) {
		h := newAnsweredOfferHandler(t, &sdkconfig.SDKConfig{}, nil, nil)
		c, ctx := answeredOfferRequest(t)
		response, errMsg := h.ExecuteModel(ctx, ModelExecutionRequest{EntryProtocol: "claude", ExitProtocol: "claude", Model: answeredOfferModel, Body: payload, AuthID: "login"})
		if errMsg != nil || string(response.Body) != "served-by-login" {
			t.Fatalf("nested call = %q, %+v", response.Body, errMsg)
		}
		if got := offered(c); got != answeredOfferStale {
			t.Fatalf("offered %+v after a plugin's own call, want the earlier %+v", got, answeredOfferStale)
		}
	})

	for _, mode := range []string{"plain", "count", "stream"} {
		t.Run("plugin executor route "+mode, func(t *testing.T) {
			host := &handlerDirectExecutorRouteHost{}
			host.hasRouters = true
			host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
				return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: "fixture-plugin"}, true
			}
			h := newAnsweredOfferHandler(t, &sdkconfig.SDKConfig{}, nil, nil)
			h.SetModelRouterHost(host)
			c, ctx := answeredOfferRequest(t)
			var ok bool
			switch mode {
			case "plain":
				_, ok = plain(h, ctx)
			case "count":
				_, ok = count(h, ctx)
			default:
				_, ok = stream(h, ctx)
			}
			if !ok || host.lastPluginID != "fixture-plugin" {
				t.Fatalf("plugin executor route: success %v, plugin %q", ok, host.lastPluginID)
			}
			if got := offered(c); got != (logging.AuthStatement{}) {
				t.Fatalf("offered %+v from a plugin executor, want none", got)
			}
		})
	}
}

// The conductor gets no way to the request's response state or a turn's sink: it reports to the execution call that
// asked it, which offers what it returns to whoever writes that result.
func TestRequestExecutionMetadataHandsTheConductorNoStatementCallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name        string
		websocket   bool
		turnContext bool
	}{
		{name: "ordinary HTTP request"},
		{name: "websocket upgrade", websocket: true},
		{name: "websocket turn", websocket: true, turnContext: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
			if test.websocket {
				ginCtx.Request.Header.Set("Connection", "Upgrade")
				ginCtx.Request.Header.Set("Upgrade", "websocket")
			}
			ctx := context.WithValue(context.Background(), "gin", ginCtx)
			var turn []logging.AuthStatement
			if test.turnContext {
				ctx = WithAnsweredAuthCallback(ctx, func(kind, provider, host string) {
					turn = append(turn, logging.AuthStatement{Kind: kind, Provider: provider, Host: host})
				})
			}

			if _, exists := requestExecutionMetadata(ctx)[coreexecutor.AnsweredAuthCallbackMetadataKey]; exists {
				t.Fatalf("metadata carries %s", coreexecutor.AnsweredAuthCallbackMetadataKey)
			}

			// The offer reaches the writer of the result: the turn's sink, or the request's state for a plain request.
			offerAnsweredAuth(ctx, false, answeredOfferKey)
			got := offered(ginCtx)
			switch {
			case test.turnContext:
				if len(turn) != 1 || turn[0] != answeredOfferKey || got != (logging.AuthStatement{}) {
					t.Fatalf("turn %+v, request %+v", turn, got)
				}
			case test.websocket:
				if got != (logging.AuthStatement{}) {
					t.Fatalf("upgrade request offered %+v", got)
				}
			default:
				if got != answeredOfferKey {
					t.Fatalf("request offered %+v, want %+v", got, answeredOfferKey)
				}
			}
		})
	}
}
