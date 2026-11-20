package live

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// unreachableExecutor fails every upstream request before any upstream answers.
type unreachableExecutor struct{ *captureExecutor }

func (unreachableExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("fixture upstream unreachable")
}

func answeredLiveCredential() *auth.Auth {
	return &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "made-up-token"},
	}
}

func assertLiveAuthOrigin(t *testing.T, headers http.Header, named bool) {
	t.Helper()
	want := map[string]string{logging.CPAAuthKindHeader: "", logging.CPAAuthProviderHeader: ""}
	if named {
		want = map[string]string{logging.CPAAuthKindHeader: "oauth", logging.CPAAuthProviderHeader: "codex"}
	}
	for name, value := range want {
		if got := headers.Get(name); got != value {
			t.Fatalf("%s = %q, want %q", name, got, value)
		}
	}
	if _, present := headers[http.CanonicalHeaderKey(logging.CPAAuthHostHeader)]; present {
		t.Fatalf("%s present for a login", logging.CPAAuthHostHeader)
	}
}

// Call creation names the credential when its upstream answered, and none when the request failed before any
// upstream answered.
func TestHandlerNamesTheCredentialOnlyOnTheUpstreamAnswer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name        string
		unreachable bool
		status      int
	}{
		{name: "answered", status: http.StatusCreated},
		{name: "upstream unreachable", unreachable: true, status: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := auth.NewManager(nil, &apiKeyFirstSelector{}, nil)
			capture := &captureExecutor{responseBody: io.NopCloser(strings.NewReader("v=0\r\na=ice-lite\r\n"))}
			if test.unreachable {
				manager.RegisterExecutor(unreachableExecutor{capture})
			} else {
				manager.RegisterExecutor(capture)
			}
			registerCredential(t, manager, answeredLiveCredential())
			handler := NewHandler(manager, nil)
			router := gin.New()
			router.Use(logging.CPATraceIDMiddleware())
			router.POST("/v1/live", handler.Handle)

			const boundary = "answered-boundary"
			req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(multipartBody(boundary, "v=0\r\na=setup:actpass", `{"model":"gpt-live-1-codex"}`)))
			req.Header.Set("Authorization", "Bearer downstream-api-key")
			req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			assertLiveAuthOrigin(t, recorder.Header(), !test.unreachable)
		})
	}
}

// Hangup names the credential when its upstream answered, and none when the request failed before any upstream
// answered.
func TestHandleHangupNamesTheCredentialOnlyOnTheUpstreamAnswer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name        string
		unreachable bool
		status      int
	}{
		{name: "answered", status: http.StatusOK},
		{name: "upstream unreachable", unreachable: true, status: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := auth.NewManager(nil, nil, nil)
			capture := &captureExecutor{statusCode: http.StatusOK, responseBody: io.NopCloser(strings.NewReader(`{"status":"ok"}`))}
			if test.unreachable {
				manager.RegisterExecutor(unreachableExecutor{capture})
			} else {
				manager.RegisterExecutor(capture)
			}
			registerCredential(t, manager, answeredLiveCredential())
			handler := NewHandler(manager, nil)
			handler.sessions.put("call-123", liveSession{authID: "codex-oauth", model: defaultLiveModel, ownerPrincipal: "owner-key", ownerProvider: "static"})
			router := gin.New()
			router.Use(logging.CPATraceIDMiddleware())
			router.POST("/v1/realtime/calls/:call_id/hangup", func(c *gin.Context) {
				c.Set("userApiKey", "owner-key")
				c.Set("accessProvider", "static")
				c.Next()
			}, handler.HandleHangup)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			assertLiveAuthOrigin(t, recorder.Header(), !test.unreachable)
		})
	}
}

// A sideband dial failure names the credential only when the upstream's own 401 handshake is relayed.
func TestHandleSidebandDialErrorNamesTheCredentialOnlyOnARelayedRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name     string
		response *http.Response
		named    bool
	}{
		{name: "relayed 401", response: &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unauthorized"))}, named: true},
		{name: "proxy error for a 503", response: &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("busy"))}},
		{name: "no handshake"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(logging.CPATraceIDMiddleware())
			router.GET("/sideband", func(c *gin.Context) {
				handleSidebandDialError(c, context.Background(), &config.Config{}, answeredLiveCredential(), test.response, errors.New("websocket: bad handshake"))
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sideband", nil))
			assertLiveAuthOrigin(t, recorder.Header(), test.named)
		})
	}
}
