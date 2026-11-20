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
		SetGinCPAAuthKind(c, "oauth")
		c.Status(http.StatusOK)
	})
	engine.GET("/fallback", func(c *gin.Context) {
		// The first credential failed before anything was sent; the second one answered.
		callback := GinCPAAuthKindCallback(c)
		callback("oauth")
		callback("apikey")
		c.Status(http.StatusOK)
	})
	engine.GET("/unknown", func(c *gin.Context) {
		SetGinCPAAuthKind(c, "")
		c.Status(http.StatusOK)
	})
	engine.GET("/committed", func(c *gin.Context) {
		c.Writer.WriteHeaderNow()
		SetGinCPAAuthKind(c, "apikey")
	})

	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/oauth", want: "oauth"},
		{path: "/fallback", want: "apikey"},
		{path: "/unknown", want: ""},
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
		SetGinCPAAuthProvider(c, "openai-compatible-lab", "api.example.test")
		c.Status(http.StatusOK)
	})
	engine.GET("/login", func(c *gin.Context) {
		SetGinCPAAuthProvider(c, "devin", "")
		c.Status(http.StatusOK)
	})
	engine.GET("/fallback", func(c *gin.Context) {
		// An API key with a base URL failed before anything was sent; a login of another provider answered.
		provider, host := GinCPAAuthProviderCallback(c), GinCPAAuthHostCallback(c)
		provider("claude")
		host("api.example.test")
		provider("devin")
		host("")
		c.Status(http.StatusOK)
	})
	engine.GET("/committed", func(c *gin.Context) {
		c.Writer.WriteHeaderNow()
		SetGinCPAAuthProvider(c, "claude", "api.example.test")
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

// A provider or host that is not a valid header field value is not written, and without a provider no host is: an
// HTTP/1.1 client otherwise rejects the whole response. The kind and the trace ID are unaffected.
func TestCPAAuthProviderAndHostHeadersSkipInvalidValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name         string
		provider     string
		host         string
		wantProvider string
		wantHost     string
	}{
		{name: "control character in the provider", provider: "openai-compatible-study\x7fteam", host: "api.example.test"},
		{name: "line break in the provider", provider: "claude\r\nX-Injected: 1", host: "api.example.test"},
		{name: "control character in the host", provider: "openai-compatible-lab", host: "api.example\x01.test", wantProvider: "openai-compatible-lab"},
		{name: "valid values", provider: "openai-compatible-lab", host: "api.example.test", wantProvider: "openai-compatible-lab", wantHost: "api.example.test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := gin.New()
			engine.Use(CPATraceIDMiddleware())
			engine.GET("/", func(c *gin.Context) {
				SetGinRequestID(c, "1234abcd")
				SetGinCPATraceID(c, "auth-index")
				SetGinCPAAuthKind(c, "apikey")
				SetGinCPAAuthProvider(c, test.provider, test.host)
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
			if got := response.Header.Get(CPAAuthKindHeader); got != "apikey" {
				t.Fatalf("%s = %q, want apikey", CPAAuthKindHeader, got)
			}
			if got := response.Header.Get(CPATraceIDHeader); got == "" {
				t.Fatalf("%s missing", CPATraceIDHeader)
			}
			if got := response.Header.Get("X-Injected"); got != "" {
				t.Fatalf("X-Injected = %q", got)
			}
			for name, want := range map[string]string{CPAAuthProviderHeader: test.wantProvider, CPAAuthHostHeader: test.wantHost} {
				values, present := response.Header[http.CanonicalHeaderKey(name)]
				if present != (want != "") || (present && values[0] != want) {
					t.Fatalf("%s = %q (present %v), want %q", name, values, present, want)
				}
			}
		})
	}
}
