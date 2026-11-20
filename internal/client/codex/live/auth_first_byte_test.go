package live

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// liveCommitRecorder records, at the first WriteHeader it receives, the header snapshot and status, and whether a
// Write followed (with which bytes) before the response ended.
type liveCommitRecorder struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	committed bool
	snapshot  http.Header
	status    int
	nextKnown bool
	nextFlush bool
	nextBytes []byte
}

func (r *liveCommitRecorder) commit(status int) {
	if !r.committed {
		r.committed, r.snapshot, r.status = true, r.ResponseRecorder.Header().Clone(), status
	}
}

func (r *liveCommitRecorder) WriteHeader(status int) {
	r.mu.Lock()
	r.commit(status)
	r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(status)
}

func (r *liveCommitRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	r.commit(http.StatusOK)
	if !r.nextKnown {
		r.nextKnown, r.nextBytes = true, append([]byte(nil), data...)
	}
	r.mu.Unlock()
	return r.ResponseRecorder.Write(data)
}

func (r *liveCommitRecorder) Flush() {
	r.mu.Lock()
	r.commit(http.StatusOK)
	if !r.nextKnown {
		r.nextKnown, r.nextFlush = true, true
	}
	r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

// assertRelayCommit: a relay names the credential it holds only on the commit that carries its upstream's response
// (that response's status and body; with an empty body, the commit at the end of the handler), and nothing on an
// error the proxy writes itself.
func assertRelayCommit(t *testing.T, r *liveCommitRecorder, named bool, upstreamStatus int, upstreamBody string) {
	t.Helper()
	got := logging.AuthStatement{Kind: r.snapshot.Get(logging.CPAAuthKindHeader), Provider: r.snapshot.Get(logging.CPAAuthProviderHeader), Host: r.snapshot.Get(logging.CPAAuthHostHeader)}
	if !named {
		if got != (logging.AuthStatement{}) {
			t.Fatalf("a proxy error names %+v; body %q", got, r.Body.String())
		}
		return
	}
	if want := (logging.AuthStatement{Kind: "oauth", Provider: "codex"}); got != want {
		t.Fatalf("relayed upstream response names %+v, want %+v", got, want)
	}
	if r.status != upstreamStatus || r.Body.String() != upstreamBody {
		t.Fatalf("named commit: status %d body %q, want the upstream's %d %q", r.status, r.Body.String(), upstreamStatus, upstreamBody)
	}
	if upstreamBody != "" && (!r.nextKnown || r.nextFlush || string(r.nextBytes) != upstreamBody[:len(r.nextBytes)]) {
		t.Fatalf("named commit made by %q (flush %v), not by the upstream body", r.nextBytes, r.nextFlush)
	}
}

// Rows 28 to 31 of the first-byte matrix: call creation, hangup, and the relayed 401 of the direct realtime
// websocket and of the sideband.
func TestRelaysNameTheCredentialOnlyOnItsUpstreamResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	owner := func(c *gin.Context) {
		c.Set("userApiKey", "owner-key")
		c.Set("accessProvider", "static")
		c.Next()
	}

	for _, test := range []struct {
		name        string
		unreachable bool
		status      int
	}{
		{name: "answered", status: http.StatusCreated},
		{name: "upstream unreachable", unreachable: true, status: http.StatusBadGateway},
	} {
		t.Run("row 28 call creation/"+test.name, func(t *testing.T) {
			manager := auth.NewManager(nil, &apiKeyFirstSelector{}, nil)
			capture := &captureExecutor{responseBody: io.NopCloser(strings.NewReader("v=0\r\na=served-by-codex-oauth\r\n"))}
			if test.unreachable {
				manager.RegisterExecutor(unreachableExecutor{capture})
			} else {
				manager.RegisterExecutor(capture)
			}
			registerCredential(t, manager, answeredLiveCredential())
			router := gin.New()
			router.Use(logging.CPATraceIDMiddleware())
			router.POST("/v1/live", NewHandler(manager, nil).Handle)
			const boundary = "first-byte-boundary"
			req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(multipartBody(boundary, "v=0\r\na=setup:actpass", `{"model":"gpt-live-1-codex"}`)))
			req.Header.Set("Authorization", "Bearer downstream-api-key")
			req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
			recorder := &liveCommitRecorder{ResponseRecorder: httptest.NewRecorder()}
			router.ServeHTTP(recorder, req)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			assertRelayCommit(t, recorder, !test.unreachable, http.StatusCreated, recorder.Body.String())
			if !test.unreachable && !strings.Contains(recorder.Body.String(), "served-by-codex-oauth") {
				t.Fatalf("body %q is not the upstream's answer", recorder.Body.String())
			}
		})
	}

	for _, test := range []struct {
		name        string
		unreachable bool
		status      int
	}{
		{name: "answered", status: http.StatusOK},
		{name: "upstream unreachable", unreachable: true, status: http.StatusBadGateway},
	} {
		t.Run("row 29 hangup/"+test.name, func(t *testing.T) {
			manager := auth.NewManager(nil, nil, nil)
			capture := &captureExecutor{statusCode: http.StatusOK, responseBody: io.NopCloser(strings.NewReader(`{"status":"served-by-codex-oauth"}`))}
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
			router.POST("/v1/realtime/calls/:call_id/hangup", owner, handler.HandleHangup)
			recorder := &liveCommitRecorder{ResponseRecorder: httptest.NewRecorder()}
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			assertRelayCommit(t, recorder, !test.unreachable, http.StatusOK, `{"status":"served-by-codex-oauth"}`)
		})
	}

	for _, row := range []string{"row 30 direct realtime websocket", "row 31 sideband"} {
		for _, test := range []struct {
			name   string
			status int
			body   string
			named  bool
		}{
			{name: "relayed 401 with a body", status: http.StatusUnauthorized, body: `{"error":"served-by-codex-oauth"}`, named: true},
			{name: "relayed 401 without a body", status: http.StatusUnauthorized, named: true},
			{name: "proxy error for an upstream 503", status: http.StatusServiceUnavailable, body: "busy"},
		} {
			t.Run(row+"/"+test.name, func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if test.body != "" {
						w.Header().Set("Content-Type", "application/json")
					}
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, test.body)
				}))
				defer upstream.Close()
				manager := auth.NewManager(nil, nil, nil)
				manager.RegisterExecutor(&captureExecutor{})
				registerCredential(t, manager, answeredLiveCredential())
				handler := NewHandler(manager, nil)
				handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/v1"
				router := gin.New()
				router.Use(logging.CPATraceIDMiddleware())
				path := "/v1/realtime?model=gpt-realtime"
				if strings.Contains(row, "sideband") {
					handler.sessions.put("call-123", liveSession{authID: "codex-oauth", model: defaultLiveModel, ownerPrincipal: "owner-key", ownerProvider: "static"})
					router.GET("/v1/live/:call_id", owner, handler.HandleSideband)
					path = "/v1/live/call-123"
				} else {
					router.GET("/v1/realtime", owner, handler.HandleRealtimeWebsocket)
				}
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
				req.Header.Set("Sec-WebSocket-Version", "13")
				req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
				recorder := &liveCommitRecorder{ResponseRecorder: httptest.NewRecorder()}
				router.ServeHTTP(recorder, req)
				if test.named && recorder.Code != test.status {
					t.Fatalf("status = %d, want the relayed %d; body=%s", recorder.Code, test.status, recorder.Body.String())
				}
				assertRelayCommit(t, recorder, test.named, test.status, test.body)
			})
		}
	}
}
