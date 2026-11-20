package logging

import (
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
