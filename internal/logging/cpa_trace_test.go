package logging

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestFormatCPATraceID(t *testing.T) {
	selectedAt := time.Date(2026, time.July, 17, 21, 58, 49, 0, time.UTC)
	got := FormatCPATraceID(selectedAt, "auth-index", "request1")
	if want := "20260717215849-auth-index-request1"; got != want {
		t.Fatalf("FormatCPATraceID() = %q, want %q", got, want)
	}

	for _, test := range []struct {
		name       string
		selectedAt time.Time
		authIndex  string
		requestID  string
	}{
		{name: "zero time", authIndex: "auth-index", requestID: "request1"},
		{name: "empty auth index", selectedAt: selectedAt, requestID: "request1"},
		{name: "empty request ID", selectedAt: selectedAt, authIndex: "auth-index"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if gotEmpty := FormatCPATraceID(test.selectedAt, test.authIndex, test.requestID); gotEmpty != "" {
				t.Fatalf("FormatCPATraceID() = %q, want empty", gotEmpty)
			}
		})
	}
}

func TestCPATraceIDMiddlewareRequiresAuthIndexBeforeResponseCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(CPATraceIDMiddleware())
	engine.GET("/selected", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		SetGinCPATraceID(c, "auth-index")
		c.Status(http.StatusOK)
	})
	engine.GET("/unselected", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		SetGinCPATraceID(c, "")
		c.Status(http.StatusOK)
	})
	engine.GET("/committed", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		c.Writer.WriteHeaderNow()
		SetGinCPATraceID(c, "auth-index")
	})

	t.Run("writes selected auth trace", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/selected", nil))

		traceID := recorder.Header().Get(CPATraceIDHeader)
		if len(traceID) != len("20060102150405-auth-index-1234abcd") {
			t.Fatalf("trace ID = %q, unexpected length", traceID)
		}
		if got := traceID[15:]; got != "auth-index-1234abcd" {
			t.Fatalf("trace suffix = %q, want %q", got, "auth-index-1234abcd")
		}
	})

	t.Run("skips empty auth index", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/unselected", nil))

		if got := recorder.Header().Get(CPATraceIDHeader); got != "" {
			t.Fatalf("trace ID = %q, want empty", got)
		}
	})

	t.Run("skips committed response", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/committed", nil))

		if got := recorder.Header().Get(CPATraceIDHeader); got != "" {
			t.Fatalf("trace ID = %q, want empty", got)
		}
	})
}

func TestCPATraceIDConcurrentSelectionAndResponseCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(CPATraceIDMiddleware())
	engine.GET("/race", func(c *gin.Context) {
		SetGinRequestID(c, "1234abcd")
		traceCallback := GinCPATraceIDCallback(c)
		start := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-start
			traceCallback("auth-index")
		}()
		close(start)
		_, _ = c.Writer.Write([]byte("\n"))
		<-done
	})

	for range 100 {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/race", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}
}

func TestCPAAuthKindHeaderNamesTheCredentialThatServedTheResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(CPATraceIDMiddleware())
	engine.GET("/oauth", func(c *gin.Context) {
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "oauth", Provider: "claude"})
		c.Status(http.StatusOK)
	})
	engine.GET("/fallback", func(c *gin.Context) {
		// The first credential failed before anything was sent; the second one answered.
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "oauth", Provider: "claude"})
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "apikey", Provider: "claude"})
		c.Status(http.StatusOK)
	})
	engine.GET("/unknown", func(c *gin.Context) {
		ConfirmGinCPAAuthStatement(c, AuthStatement{})
		c.Status(http.StatusOK)
	})
	engine.GET("/kind-alone", func(c *gin.Context) {
		// A kind without a provider is never sent: a client would read it as an older proxy and guess.
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "apikey"})
		c.Status(http.StatusOK)
	})
	engine.GET("/committed", func(c *gin.Context) {
		c.Writer.WriteHeaderNow()
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "apikey", Provider: "claude"})
	})

	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/oauth", want: "oauth"},
		{path: "/fallback", want: "apikey"},
		{path: "/unknown", want: ""},
		{path: "/kind-alone", want: ""},
		{path: "/committed", want: ""},
	} {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if got := recorder.Header().Get(CPAAuthKindHeader); got != test.want {
				t.Fatalf("%s = %q, want %q", CPAAuthKindHeader, got, test.want)
			}
		})
	}
}

func TestCPAAuthProviderAndHostHeadersNameTheCredentialThatServedTheResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(CPATraceIDMiddleware())
	engine.GET("/key", func(c *gin.Context) {
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "apikey", Provider: "openai-compatible-lab", Host: "api.example.test"})
		c.Status(http.StatusOK)
	})
	engine.GET("/login", func(c *gin.Context) {
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "oauth", Provider: "devin"})
		c.Status(http.StatusOK)
	})
	engine.GET("/fallback", func(c *gin.Context) {
		// An API key with a base URL failed before anything was sent; a login of another provider answered.
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "apikey", Provider: "claude", Host: "api.example.test"})
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "oauth", Provider: "devin"})
		c.Status(http.StatusOK)
	})
	engine.GET("/committed", func(c *gin.Context) {
		c.Writer.WriteHeaderNow()
		ConfirmGinCPAAuthStatement(c, AuthStatement{Kind: "apikey", Provider: "claude", Host: "api.example.test"})
	})

	for _, test := range []struct {
		path     string
		provider string
		host     string
	}{
		{path: "/key", provider: "openai-compatible-lab", host: "api.example.test"},
		{path: "/login", provider: "devin"},
		{path: "/fallback", provider: "devin"},
		{path: "/committed"},
	} {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if got := recorder.Header().Get(CPAAuthProviderHeader); got != test.provider {
				t.Fatalf("%s = %q, want %q", CPAAuthProviderHeader, got, test.provider)
			}
			if _, present := recorder.Header()[http.CanonicalHeaderKey(CPAAuthHostHeader)]; present != (test.host != "") {
				t.Fatalf("%s present = %v, want %v", CPAAuthHostHeader, present, test.host != "")
			}
			if got := recorder.Header().Get(CPAAuthHostHeader); got != test.host {
				t.Fatalf("%s = %q, want %q", CPAAuthHostHeader, got, test.host)
			}
		})
	}
}

// A statement is sent whole or not at all, and only in printable ASCII: a provider byte outside it (a control
// character, a line break, UTF-8), a space or "%" is percent-encoded, so an HTTP/1.1 client accepts the response and
// a header carries what the websocket's JSON carries. A host outside printable ASCII, an unknown kind or a missing
// provider sends none of the three; the trace ID is sent either way.
func TestCPAAuthStatementIsSentWholeInASCIIOrNotAtAll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name      string
		statement AuthStatement
		want      AuthStatement
	}{
		{name: "control character in the provider", statement: AuthStatement{Kind: "apikey", Provider: "openai-compatible-study\x7fteam", Host: "api.example.test"}, want: AuthStatement{Kind: "apikey", Provider: "openai-compatible-study%7Fteam", Host: "api.example.test"}},
		{name: "line break in the provider", statement: AuthStatement{Kind: "apikey", Provider: "claude\r\nX-Injected: 1", Host: "api.example.test"}, want: AuthStatement{Kind: "apikey", Provider: "claude%0D%0AX-Injected:%201", Host: "api.example.test"}},
		{name: "provider outside ASCII", statement: AuthStatement{Kind: "apikey", Provider: "openai-compatible-büro", Host: "api.example.test"}, want: AuthStatement{Kind: "apikey", Provider: "openai-compatible-b%C3%BCro", Host: "api.example.test"}},
		{name: "percent sign in the provider", statement: AuthStatement{Kind: "apikey", Provider: "lab%20team", Host: "api.example.test"}, want: AuthStatement{Kind: "apikey", Provider: "lab%2520team", Host: "api.example.test"}},
		{name: "control character in the host", statement: AuthStatement{Kind: "apikey", Provider: "openai-compatible-lab", Host: "api.example\x01.test"}},
		{name: "host outside ASCII", statement: AuthStatement{Kind: "apikey", Provider: "claude", Host: "bücher.example"}},
		{name: "unknown kind", statement: AuthStatement{Kind: "token", Provider: "claude", Host: "api.example.test"}},
		{name: "kind alone", statement: AuthStatement{Kind: "apikey"}},
		{name: "host without a provider", statement: AuthStatement{Kind: "apikey", Host: "api.example.test"}},
		{name: "login without a host", statement: AuthStatement{Kind: "oauth", Provider: "devin"}, want: AuthStatement{Kind: "oauth", Provider: "devin"}},
		{name: "IDNA host", statement: AuthStatement{Kind: "apikey", Provider: "claude", Host: "xn--bcher-kva.example"}, want: AuthStatement{Kind: "apikey", Provider: "claude", Host: "xn--bcher-kva.example"}},
		{name: "valid values", statement: AuthStatement{Kind: "apikey", Provider: "openai-compatible-lab", Host: "api.example.test"}, want: AuthStatement{Kind: "apikey", Provider: "openai-compatible-lab", Host: "api.example.test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.statement.OnTheWire(); got != test.want {
				t.Fatalf("OnTheWire() = %+q, want %+q", got, test.want)
			}
			engine := gin.New()
			engine.Use(CPATraceIDMiddleware())
			engine.GET("/", func(c *gin.Context) {
				SetGinRequestID(c, "1234abcd")
				SetGinCPATraceID(c, "auth-index")
				ConfirmGinCPAAuthStatement(c, test.statement)
				c.String(http.StatusOK, "fixture body")
			})
			// A real HTTP/1.1 server and client: the recorder would not show the client rejecting the response.
			server := httptest.NewServer(engine)
			defer server.Close()
			response, errGet := server.Client().Get(server.URL)
			if errGet != nil {
				t.Fatalf("HTTP/1.1 client rejected the response: %v", errGet)
			}
			defer func() { _ = response.Body.Close() }()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusOK || string(body) != "fixture body" {
				t.Fatalf("status=%d body=%q", response.StatusCode, body)
			}
			if got := response.Header.Get(CPATraceIDHeader); got == "" {
				t.Fatalf("%s missing", CPATraceIDHeader)
			}
			if got := response.Header.Get("X-Injected"); got != "" {
				t.Fatalf("X-Injected = %q", got)
			}
			for name, want := range map[string]string{CPAAuthKindHeader: test.want.Kind, CPAAuthProviderHeader: test.want.Provider, CPAAuthHostHeader: test.want.Host} {
				values, present := response.Header[http.CanonicalHeaderKey(name)]
				if present != (want != "") || (present && values[0] != want) {
					t.Fatalf("%s = %q (present %v), want %q", name, values, present, want)
				}
			}
		})
	}
}
