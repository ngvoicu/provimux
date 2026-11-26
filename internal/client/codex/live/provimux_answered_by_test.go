package live

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the live call relays (rows 28 to 31 of the first-byte matrix) name the account of the credential they
// hold with the marker of answered-by.md 5.2, beside the three, on the commit that carries their upstream's response
// and on nothing the proxy writes itself. The relay runs no executor, so it states no model. Fixtures are invented.

func TestProvimuxLiveRelaysNameTheAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	auth.SetProvimuxMarkerKey([]byte("provimux-live-test-marker-key-32"))
	was := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() {
		logging.SetProvimuxAnsweredBy(was)
		auth.SetProvimuxMarkerKey(nil)
	})
	marker := answeredLiveCredential().ProvimuxAccountMarker()
	if marker == "" {
		t.Fatal("fixture: no marker")
	}
	owner := func(c *gin.Context) {
		c.Set("userApiKey", "owner-key")
		c.Set("accessProvider", "static")
		c.Next()
	}
	check := func(t *testing.T, recorder *liveCommitRecorder, named bool) {
		t.Helper()
		kind, account := recorder.snapshot.Get(logging.CPAAuthKindHeader), recorder.snapshot.Values(logging.ProvimuxAccountHeader)
		if named && (kind != "oauth" || len(account) != 1 || account[0] != marker) {
			t.Fatalf("relayed answer: kind %q, account %v; want oauth and %s", kind, account, marker)
		}
		if !named && (kind != "" || len(account) != 0) {
			t.Fatalf("the proxy's own error names kind %q, account %v", kind, account)
		}
		for _, name := range logging.ProvimuxFieldNames()[1:] {
			if values := recorder.snapshot.Values(name.Header); len(values) != 0 {
				t.Fatalf("a relay states %s: %v", name.Header, values)
			}
		}
	}

	for _, unreachable := range []bool{false, true} {
		t.Run("row 28 call creation", func(t *testing.T) {
			manager := auth.NewManager(nil, &apiKeyFirstSelector{}, nil)
			capture := &captureExecutor{responseBody: io.NopCloser(strings.NewReader("v=0\r\na=served-by-codex-oauth\r\n"))}
			if unreachable {
				manager.RegisterExecutor(unreachableExecutor{capture})
			} else {
				manager.RegisterExecutor(capture)
			}
			registerCredential(t, manager, answeredLiveCredential())
			router := gin.New()
			router.Use(logging.CPATraceIDMiddleware())
			router.POST("/v1/live", NewHandler(manager, nil).Handle)
			const boundary = "provimux-boundary"
			req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(multipartBody(boundary, "v=0\r\na=setup:actpass", `{"model":"gpt-live-1-codex"}`)))
			req.Header.Set("Authorization", "Bearer downstream-api-key")
			req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
			recorder := &liveCommitRecorder{ResponseRecorder: httptest.NewRecorder()}
			router.ServeHTTP(recorder, req)
			check(t, recorder, !unreachable)
		})
		t.Run("row 29 hangup", func(t *testing.T) {
			manager := auth.NewManager(nil, nil, nil)
			capture := &captureExecutor{statusCode: http.StatusOK, responseBody: io.NopCloser(strings.NewReader(`{"status":"served-by-codex-oauth"}`))}
			if unreachable {
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
			check(t, recorder, !unreachable)
		})
	}

	for _, row := range []string{"row 30 direct realtime websocket", "row 31 sideband"} {
		for _, test := range []struct {
			status int
			named  bool
		}{{status: http.StatusUnauthorized, named: true}, {status: http.StatusServiceUnavailable}} {
			t.Run(row, func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, `{"error":"served-by-codex-oauth"}`)
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
				check(t, recorder, test.named)
			})
		}
	}
}
