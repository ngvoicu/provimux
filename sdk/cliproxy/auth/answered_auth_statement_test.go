package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The conductor's publication and the header commit race: the publisher switches between a hosted key, a login and
// no statement while the handler commits its headers (as a keep-alive would). Every response carries one whole
// statement or none: never a kind without its provider, or a provider without its host.
func TestAnsweredAuthStatementIsCommittedWholeOrNotAtAll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hostedKey := &Auth{Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "made-up-key", "base_url": "https://openrouter.example.test/api/v1"}}
	login := &Auth{Provider: "devin", Attributes: map[string]string{AttributeAuthKind: "oauth"}}
	allowed := map[[3]string]bool{
		{"", "", ""}: true,
		{"apikey", "claude", "openrouter.example.test"}: true,
		{"oauth", "devin", ""}:                          true,
	}

	iteration := 0
	engine := gin.New()
	engine.Use(logging.CPATraceIDMiddleware())
	engine.GET("/", func(c *gin.Context) {
		meta := map[string]any{cliproxyexecutor.AnsweredAuthCallbackMetadataKey: func(kind, provider, host string) {
			logging.ConfirmGinCPAAuthStatement(c, logging.AuthStatement{Kind: kind, Provider: provider, Host: host})
		}}
		mode := iteration % 4
		started := make(chan struct{})
		done := make(chan struct{})
		go func() {
			close(started)
			if mode == 2 {
				runtime.Gosched()
			}
			for i := 0; i < 8; i++ {
				switch i % 3 {
				case 0:
					publishAnsweredAuthMetadata(upstreamAnswered(), meta, hostedKey)
				case 1:
					publishAnsweredAuthMetadata(upstreamAnswered(), meta, login)
				default:
					// No upstream exchange: the statement is withdrawn.
					publishAnsweredAuthMetadata(cliproxyexecutor.WithUpstreamAttemptTracker(context.Background()), meta, hostedKey)
				}
			}
			close(done)
		}()
		<-started
		if mode == 1 || mode == 3 {
			runtime.Gosched()
		}
		_, _ = c.Writer.Write([]byte("\n"))
		c.Writer.Flush()
		<-done
	})

	seen := map[[3]string]int{}
	for iteration = 0; iteration < 20000; iteration++ {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
		header := recorder.Result().Header
		got := [3]string{header.Get(logging.CPAAuthKindHeader), header.Get(logging.CPAAuthProviderHeader), header.Get(logging.CPAAuthHostHeader)}
		if !allowed[got] {
			t.Fatalf("response %d carries part of a statement: kind=%q provider=%q host=%q", iteration, got[0], got[1], got[2])
		}
		seen[got]++
	}
	for statement, count := range seen {
		t.Logf("kind=%q provider=%q host=%q: %d", statement[0], statement[1], statement[2], count)
	}
	if len(seen) < 2 {
		t.Fatalf("the publisher and the commit never overlapped: %v", seen)
	}
}

// upstreamAnswered returns an attempt context whose tracker recorded an upstream exchange, as an executor's
// transport does when it sends a request.
func upstreamAnswered() context.Context {
	ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	return ctx
}

// A response no upstream produced (a count the proxy estimated itself) names no credential, and withdraws any
// statement published before it.
func TestAnsweredAuthStatementNeedsAnUpstreamExchange(t *testing.T) {
	var got logging.AuthStatement
	meta := map[string]any{cliproxyexecutor.AnsweredAuthCallbackMetadataKey: func(kind, provider, host string) {
		got = logging.AuthStatement{Kind: kind, Provider: provider, Host: host}
	}}
	key := &Auth{Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "made-up-key", "base_url": "https://openrouter.example.test/api/v1"}}
	publishAnsweredAuthMetadata(upstreamAnswered(), meta, key)
	if want := (logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "openrouter.example.test"}); got != want {
		t.Fatalf("statement after an upstream answer = %+v, want %+v", got, want)
	}
	publishAnsweredAuthMetadata(cliproxyexecutor.WithUpstreamAttemptTracker(context.Background()), meta, key)
	if got != (logging.AuthStatement{}) {
		t.Fatalf("statement without an upstream exchange = %+v, want none", got)
	}
}

// preparedHomeDispatch hands out an API key whose base URL its executor's preparation replaces.
type preparedHomeDispatch struct {
	homeOAuthLoggingDispatcher
	original string
}

func (d preparedHomeDispatch) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{ID: "prepared-home-key", Provider: "home-execution", Status: StatusActive, Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey, AttributeAPIKey: "made-up-key", "base_url": d.original}}})
}

// preparedHomeExecutor moves the credential to another endpoint when preparing it, then sends the request to the
// credential's base URL.
type preparedHomeExecutor struct {
	homeExecutionExecutor
	replacement string
}

func (*preparedHomeExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (e *preparedHomeExecutor) PrepareRequestAuth(_ context.Context, a *Auth) (*Auth, error) {
	prepared := a.Clone()
	prepared.Attributes["base_url"] = e.replacement
	return prepared, nil
}

func (*preparedHomeExecutor) Execute(ctx context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, a.Attributes["base_url"], nil)
	if errRequest != nil {
		return cliproxyexecutor.Response{}, errRequest
	}
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	res, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		return cliproxyexecutor.Response{}, errDo
	}
	defer func() { _ = res.Body.Close() }()
	body, errRead := io.ReadAll(res.Body)
	return cliproxyexecutor.Response{Payload: body}, errRead
}

func (e *preparedHomeExecutor) CountTokens(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, a, req, opts)
}

func (e *preparedHomeExecutor) ExecuteStream(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	resp, errExec := e.Execute(ctx, a, req, opts)
	if errExec != nil {
		return nil, errExec
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

// Where preparation replaces the credential, the statement names the prepared one, the credential the request went
// out with: on the Home plain, count and stream paths alike.
func TestAnsweredAuthStatementNamesThePreparedHomeCredential(t *testing.T) {
	for _, operation := range []string{"plain", "count", "stream"} {
		t.Run(operation, func(t *testing.T) {
			var originalCalls, replacementCalls atomic.Int32
			original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originalCalls.Add(1)
				_, _ = io.WriteString(w, "original endpoint")
			}))
			t.Cleanup(original.Close)
			replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				replacementCalls.Add(1)
				_, _ = io.WriteString(w, "replacement endpoint")
			}))
			t.Cleanup(replacement.Close)

			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(preparedHomeDispatch{original: strings.Replace(original.URL, "127.0.0.1", "localhost", 1)}, executionregistry.New(), 1)
			manager.RegisterExecutor(&preparedHomeExecutor{replacement: replacement.URL})

			var statement logging.AuthStatement
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.AnsweredAuthCallbackMetadataKey: func(kind, provider, host string) {
				statement = logging.AuthStatement{Kind: kind, Provider: provider, Host: host}
			}}}
			ctx, providers, req := context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "prepared-model"}
			var payload []byte
			var errExec error
			switch operation {
			case "plain":
				var resp cliproxyexecutor.Response
				resp, errExec = manager.Execute(ctx, providers, req, opts)
				payload = resp.Payload
			case "count":
				var resp cliproxyexecutor.Response
				resp, errExec = manager.ExecuteCount(ctx, providers, req, opts)
				payload = resp.Payload
			case "stream":
				var stream *cliproxyexecutor.StreamResult
				stream, errExec = manager.ExecuteStream(ctx, providers, req, opts)
				if errExec == nil {
					for chunk := range stream.Chunks {
						payload = append(payload, chunk.Payload...)
					}
				}
			}
			if errExec != nil || string(payload) != "replacement endpoint" || originalCalls.Load() != 0 || replacementCalls.Load() != 1 {
				t.Fatalf("err=%v payload=%q original calls=%d replacement calls=%d", errExec, payload, originalCalls.Load(), replacementCalls.Load())
			}
			if want := (logging.AuthStatement{Kind: "apikey", Provider: "home-execution", Host: "127.0.0.1"}); statement != want {
				t.Fatalf("statement = %+v, want %+v (the prepared credential's host)", statement, want)
			}
		})
	}
}

type answeredOnceUnavailable struct{}

func (answeredOnceUnavailable) Error() string   { return "fixture upstream unavailable" }
func (answeredOnceUnavailable) StatusCode() int { return http.StatusServiceUnavailable }

// answeredOnceExecutor stands for an upstream: every attempt marks its exchange; a credential in fail answers with an
// error.
type answeredOnceExecutor struct {
	provider string
	fail     map[string]bool
}

func (e *answeredOnceExecutor) Identifier() string { return e.provider }

func (e *answeredOnceExecutor) Execute(ctx context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if e.fail[a.ID] {
		return cliproxyexecutor.Response{}, answeredOnceUnavailable{}
	}
	return cliproxyexecutor.Response{Payload: []byte("served-by-" + a.ID)}, nil
}

func (e *answeredOnceExecutor) CountTokens(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, a, req, opts)
}

func (e *answeredOnceExecutor) ExecuteStream(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	resp, errExec := e.Execute(ctx, a, req, opts)
	if errExec != nil {
		return nil, errExec
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *answeredOnceExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }

func (e *answeredOnceExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, answeredOnceUnavailable{}
}

// The answered-auth callback is called exactly once per Execute, ExecuteCount or ExecuteStream call that returns a
// response, with the statement of the credential that produced it (after a failed first credential, the second's);
// never at a selection, and never for a call that returns an error.
func TestAnsweredAuthCallbackIsCalledOncePerReturnedResponse(t *testing.T) {
	const model = "answered-once-model"
	key := &Auth{ID: "once-key", Provider: "claude", Status: StatusActive, Attributes: map[string]string{AttributeAPIKey: "made-up-key", "base_url": "https://openrouter.example.test/v1", "priority": "10"}}
	login := &Auth{ID: "once-login", Provider: "devin", Status: StatusActive, Attributes: map[string]string{AttributeAuthKind: "oauth", "priority": "0"}}
	for _, operation := range []string{"plain", "count", "stream"} {
		for _, test := range []struct {
			name  string
			fail  map[string]bool
			calls []logging.AuthStatement
		}{
			{name: "first answers", calls: []logging.AuthStatement{{Kind: "apikey", Provider: "claude", Host: "openrouter.example.test"}}},
			{name: "first fails, second answers", fail: map[string]bool{"once-key": true}, calls: []logging.AuthStatement{{Kind: "oauth", Provider: "devin"}}},
			{name: "all fail", fail: map[string]bool{"once-key": true, "once-login": true}},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 3)
				for _, credential := range []*Auth{key, login} {
					manager.RegisterExecutor(&answeredOnceExecutor{provider: credential.Provider, fail: test.fail})
					if _, err := manager.Register(context.Background(), credential.Clone()); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
					id := credential.ID
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}
				var calls []logging.AuthStatement
				selections := 0
				opts := cliproxyexecutor.Options{Metadata: map[string]any{
					cliproxyexecutor.AnsweredAuthCallbackMetadataKey: func(kind, provider, host string) {
						calls = append(calls, logging.AuthStatement{Kind: kind, Provider: provider, Host: host})
					},
					cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(string) { selections++ },
				}}
				providers, req := []string{"claude", "devin"}, cliproxyexecutor.Request{Model: model}
				var errExec error
				switch operation {
				case "plain":
					_, errExec = manager.Execute(context.Background(), providers, req, opts)
				case "count":
					_, errExec = manager.ExecuteCount(context.Background(), providers, req, opts)
				default:
					var stream *cliproxyexecutor.StreamResult
					stream, errExec = manager.ExecuteStream(context.Background(), providers, req, opts)
					if errExec == nil {
						for range stream.Chunks {
						}
					}
				}
				if (errExec == nil) != (len(test.calls) == 1) {
					t.Fatalf("error = %v", errExec)
				}
				if selections == 0 {
					t.Fatalf("no selection was made")
				}
				if fmt.Sprint(calls) != fmt.Sprint(test.calls) {
					t.Fatalf("answered callback calls = %+v over %d selections, want %+v", calls, selections, test.calls)
				}
			})
		}
	}
}
