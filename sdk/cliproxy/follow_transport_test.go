package cliproxy

import (
	"context"
	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type followReceived struct{ host, path, model, body string }

func followTLS(t *testing.T) (*http.Transport, chan followReceived) {
	t.Helper()
	got := make(chan followReceived, 12)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m := ojson.GetBytes(b, "model").String()
		got <- followReceived{r.Host, r.URL.Path, m, string(b)}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reviewChat)
	}))
	t.Cleanup(s.Close)
	tr := s.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig.ServerName = "example.com"
	tr.DialContext = func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "tcp", s.Listener.Addr().String())
	}
	t.Cleanup(tr.CloseIdleConnections)
	return tr, got
}
func followDrained(c chan followReceived) []followReceived {
	var v []followReceived
	for len(c) > 0 {
		v = append(v, <-c)
	}
	return v
}

// Findings 32, 34 and 35, unchanged: an executor registered through the SDK answers (rule A.1), so no mode names an
// entry; body-readback would accept the entry of the bytes the transport read (rule A.4).
func TestFollowAnsweringChannel(t *testing.T) {
	for _, mode := range []string{"socket-only", "preopened-socket", "separate-untraced", "other-executors-catalog", "lookalike-discovery", "overridden-housekeeping-authority", "body-readback"} {
		t.Run(mode, func(t *testing.T) {
			h := newSentHarness(t, reviewRouter)
			useAnsweredBook(t, newSentClock())
			tr, receipts := followTLS(t)
			reviewSet(h, tr)
			var conn *websocket.Conn
			sockSeen := make(chan string, 1)
			if strings.Contains(mode, "socket") {
				sock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					u := websocket.Upgrader{}
					c, e := u.Upgrade(w, r, nil)
					if e != nil {
						return
					}
					defer c.Close()
					_, b, e := c.ReadMessage()
					if e == nil {
						sockSeen <- ojson.GetBytes(b, "model").String()
						c.WriteMessage(websocket.TextMessage, []byte(reviewChat))
					}
				}))
				defer sock.Close()
				var err error
				conn, _, err = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(sock.URL, "http"), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
			}
			a := sentKeyFor(t, h, "review-public")
			orig, _ := h.service.coreManager.Executor(a.Provider)
			h.service.coreManager.RegisterExecutor(reviewExec{ProviderExecutor: orig, run: func(c context.Context, a *ca.Auth, _ ex.Request, _ ex.Options) (ex.Response, error) {
				if mode != "socket-only" && mode != "body-readback" {
					r, e := reviewSend(c, tr, "https://openrouter.ai/v1/chat/completions", "lab/first")
					if e != nil {
						return ex.Response{}, e
					}
					r.Body.Close()
				}
				var data []byte
				if conn != nil {
					if e := conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"lab/socket"}`)); e != nil {
						return ex.Response{}, e
					}
					_, b, e := conn.ReadMessage()
					if e != nil {
						return ex.Response{}, e
					}
					data = b
				} else {
					method, dest, raw, media := "POST", "https://openrouter.ai/v1/chat/completions", `{"model":"lab/actual"}`, "application/json"
					switch mode {
					case "other-executors-catalog":
						method, dest, raw = "GET", "https://api.githubcopilot.com/models", ""
					case "lookalike-discovery":
						dest, raw, media = "https://cloudcode-pa.googleapis.com/tenant/v1internal:loadCodeAssist", "content=answer", "application/x-www-form-urlencoded"
					case "overridden-housekeeping-authority":
						dest, raw, media = "https://oauth2.googleapis.com/token", "content=answer", "application/x-www-form-urlencoded"
					case "body-readback":
						raw = `{"model":"lab/first"}`
					}
					req, _ := http.NewRequestWithContext(c, method, dest, strings.NewReader(raw))
					req.Header.Set("Content-Type", media)
					if mode == "overridden-housekeeping-authority" {
						req.Host = "routing.fixture.invalid"
					}
					if mode == "body-readback" {
						s := `{"model":"lab/other"}`
						req.Body = io.NopCloser(strings.NewReader(s))
						req.ContentLength = int64(len(s))
					}
					client := httptransport.Decorate(c, &http.Client{Transport: tr})
					if mode == "separate-untraced" {
						client = &http.Client{Transport: reviewRT(func(r *http.Request) (*http.Response, error) { return tr.RoundTrip(r.Clone(context.Background())) })}
					}
					res, e := client.Do(req)
					if e != nil {
						return ex.Response{}, e
					}
					data, _ = io.ReadAll(res.Body)
					res.Body.Close()
				}
				ex.MarkUpstreamAttempt(c)
				return ex.Response{Payload: data}, nil
			}})
			ans := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
			if ans.status != 200 || ans.get(logging.ProvimuxAccountHeader) == "" {
				t.Fatalf("fixture %d, %s", ans.status, ans.body)
			}
			received := followDrained(receipts)
			socket := ""
			if conn != nil {
				socket = <-sockSeen
			}
			count := 2
			if mode == "socket-only" {
				count = 0
			} else if mode == "preopened-socket" || mode == "body-readback" {
				count = 1
			}
			if len(received) != count {
				t.Fatalf("fixture saw %d HTTP calls, want %d", len(received), count)
			}
			entry := ans.get(logging.ProvimuxEntryHeader)
			t.Logf("received=%v socket=%q client entry=%q", received, socket, entry)
			if entry != "" && !(mode == "body-readback" && entry == "openrouter/lab/other") {
				t.Error("entry did not come from the answering request")
			}
		})
	}
}
