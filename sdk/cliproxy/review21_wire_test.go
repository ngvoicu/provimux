package cliproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

type reviewRT func(*http.Request) (*http.Response, error)

func (f reviewRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// reviewSet puts rt on the manager as every credential's transport: a test's stand-in network.
func reviewSet(h *sentHarness, rt http.RoundTripper) {
	h.service.coreManager.SetRoundTripperProvider(copilotAccountTransport(func(*ca.Auth) http.RoundTripper { return rt }))
}

func reviewOK(req *http.Request, s, ct string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(s)), Request: req}
}

const reviewChat = `{"id":"chatcmpl_fixture","object":"chat.completion","model":"review","choices":[{"index":0,"message":{"role":"assistant","content":"fixture answer"},"finish_reason":"stop"}]}`
const reviewRouter = `api-keys:
  openai-compatibility:
    - name: review-router
      base-url: https://openrouter.ai/api/v1
      models:
        - name: lab/native
          alias: review-public
      keys:
        - api-key: review-synthetic-key
`

func reviewGet(t *testing.T, h *sentHarness, path, key string) []byte {
	t.Helper()
	req, _ := http.NewRequest("GET", h.server.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	r, e := h.server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 {
		t.Fatalf("GET %s %d", path, r.StatusCode)
	}
	return b
}

// Real TLS vendor sees Go's serialized authority; the relay tunnels byte for byte.
func TestReview21AuthorityAndProxy(t *testing.T) {
	for _, override := range []string{"", "routing.fixture.invalid"} {
		t.Run("host-"+override, func(t *testing.T) {
			cfg := reviewRouter
			if override != "" {
				cfg = strings.Replace(cfg, "      models:", "      headers:\n        Host: "+override+"\n      models:", 1)
			}
			h := newSentHarness(t, cfg)
			useAnsweredBook(t, newSentClock())
			seen := make(chan string, 1)
			vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				seen <- r.Host + " " + ojson.GetBytes(b, "model").String()
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, reviewChat)
			}))
			defer vendor.Close()
			tunnel := make(chan string, 1)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" {
					http.Error(w, "CONNECT required", 400)
					return
				}
				tunnel <- r.Host
				dst, err := net.Dial("tcp", vendor.Listener.Addr().String())
				if err != nil {
					http.Error(w, "fixture", 500)
					return
				}
				src, bw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					dst.Close()
					return
				}
				fmt.Fprint(bw, "HTTP/1.1 200 Connection Established\r\n\r\n")
				bw.Flush()
				go func() { defer dst.Close(); _, _ = io.Copy(dst, src) }()
				defer src.Close()
				_, _ = io.Copy(src, dst)
			}))
			defer proxy.Close()
			u, _ := url.Parse(proxy.URL)
			tr := vendor.Client().Transport.(*http.Transport).Clone()
			tr.Proxy = http.ProxyURL(u)
			tr.TLSClientConfig.ServerName = "example.com"
			defer tr.CloseIdleConnections()
			reviewSet(h, tr)
			ans := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
			if ans.status != 200 {
				t.Fatalf("fixture HTTP %d %s", ans.status, ans.body)
			}
			authority, connect := <-seen, <-tunnel
			wantHost := "openrouter.ai"
			if override != "" {
				wantHost = override
			}
			if authority != wantHost+" lab/native" || connect != "openrouter.ai:443" {
				t.Fatalf("fixture vendor %s, CONNECT %s", authority, connect)
			}
			got := ans.get(logging.ProvimuxEntryHeader)
			want := "openrouter/lab/native"
			if override != "" {
				want = ""
			}
			t.Logf("CONNECT %s, receiving vendor %q, answer %q", connect, authority, got)
			if got != want {
				t.Errorf("authority disagreement must withhold entry; want %q", want)
			}
		})
	}
}

// Answer seen on a WebSocket plus successful HTTP work in one supported SDK execution.
// This is a composed executor, independent of which paths the built-in executors combine today.
type reviewExec struct {
	ca.ProviderExecutor
	run     func(context.Context, *ca.Auth, ex.Request, ex.Options) (ex.Response, error)
	refresh func(context.Context, *ca.Auth) (*ca.Auth, error)
}

func (e reviewExec) Execute(c context.Context, a *ca.Auth, r ex.Request, o ex.Options) (ex.Response, error) {
	return e.run(c, a, r, o)
}
func (e reviewExec) Refresh(c context.Context, a *ca.Auth) (*ca.Auth, error) {
	if e.refresh != nil {
		return e.refresh(c, a)
	}
	return e.ProviderExecutor.Refresh(c, a)
}
func reviewSend(c context.Context, rt http.RoundTripper, dest, model string) (*http.Response, error) {
	r, _ := http.NewRequestWithContext(c, "POST", dest, strings.NewReader(fmt.Sprintf(`{"model":%q}`, model)))
	r.Header.Set("Content-Type", "application/json")
	return httptransport.Decorate(c, &http.Client{Transport: rt}).Do(r)
}
func TestReview21MixedAndTwoRequests(t *testing.T) {
	for _, kind := range []string{"socket", "detached-http", "different-http", "same-http", "no-model", "wrong-publisher", "unread", "other-devin-method", "status-fail"} {
		t.Run(kind, func(t *testing.T) {
			h := newSentHarness(t, reviewRouter)
			useAnsweredBook(t, newSentClock())
			wsSeen := make(chan string, 1)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				u := websocket.Upgrader{}
				c, e := u.Upgrade(w, r, nil)
				if e != nil {
					return
				}
				defer c.Close()
				_, b, e := c.ReadMessage()
				if e != nil {
					return
				}
				wsSeen <- ojson.GetBytes(b, "model").String()
				c.WriteMessage(websocket.TextMessage, []byte(reviewChat))
			}))
			defer up.Close()
			a := sentKeyFor(t, h, "review-public")
			orig, _ := h.service.coreManager.Executor(a.Provider)
			var received []string
			rt := reviewRT(func(r *http.Request) (*http.Response, error) {
				b, _ := io.ReadAll(r.Body)
				r.Body.Close()
				received = append(received, r.URL.Host+" "+string(b))
				res := reviewOK(r, reviewChat, "application/json")
				if strings.HasSuffix(r.URL.Path, "/failed") {
					res.StatusCode = 503
				}
				return res, nil
			})
			h.service.coreManager.RegisterExecutor(reviewExec{ProviderExecutor: orig, run: func(c context.Context, a *ca.Auth, _ ex.Request, _ ex.Options) (ex.Response, error) {
				first, err := reviewSend(c, rt, "https://openrouter.ai/v1/chat/completions", "lab/first")
				if err != nil {
					return ex.Response{}, err
				}
				first.Body.Close()
				var out = []byte(reviewChat)
				switch kind {
				case "socket":
					conn, _, e := websocket.DefaultDialer.DialContext(c, "ws"+strings.TrimPrefix(up.URL, "http"), nil)
					if e != nil {
						return ex.Response{}, e
					}
					defer conn.Close()
					conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"lab/second"}`))
					_, out, err = conn.ReadMessage()
					if err != nil {
						return ex.Response{}, err
					}
				default:
					ctx := c
					if kind == "detached-http" {
						ctx = context.Background()
					}
					dest := "https://openrouter.ai/v1/chat/completions"
					body := `{"model":"lab/second"}`
					ct := "application/json"
					switch kind {
					case "same-http":
						body = `{"model":"lab/first"}`
					case "no-model":
						body = `{"messages":[]}`
					case "wrong-publisher":
						dest = "https://us-aiplatform.googleapis.com/v1/projects/a/locations/us/publishers/acme/models/b:predict"
					case "unread":
						ct = "multipart/form-data; boundary=example"
					case "other-devin-method":
						ct = "application/connect+proto"
						dest = "https://server.codeium.com/exa.api_server_pb.ApiServerService/Other"
						body = "\x00\x00\x00\x00\x03\xaa\x01\x00"
					case "status-fail":
						dest = "https://openrouter.ai/failed"
					}
					r, _ := http.NewRequestWithContext(ctx, "POST", dest, strings.NewReader(body))
					r.Header.Set("Content-Type", ct)
					res, e := httptransport.Decorate(c, &http.Client{Transport: rt}).Do(r)
					if e != nil {
						return ex.Response{}, e
					}
					out, _ = io.ReadAll(res.Body)
					res.Body.Close()
				}
				ex.MarkUpstreamAttempt(c)
				return ex.Response{Payload: out}, nil
			}})
			ans := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
			if ans.status != 200 || ans.get(logging.ProvimuxAccountHeader) == "" {
				t.Fatalf("fixture %d %v %s", ans.status, ans.header, ans.body)
			}
			if kind == "socket" && <-wsSeen != "lab/second" {
				t.Fatal("socket fixture")
			}
			if kind != "socket" && len(received) != 2 {
				t.Fatalf("fixture %v", received)
			}
			got := ans.get(logging.ProvimuxEntryHeader)
			// Ported (rule 1 of the lead's 2026-11-27 amendment: exactly one request that could have produced the
			// answer): two answered requests for the same model are two, and name nothing; a refused second is none.
			// Ported again (rule A.1 of the amendment of 2026-11-27 (2)): this executor is registered through the SDK,
			// not by the gateway's service, so no case names an entry; a refused request beside the answering one is
			// pinned on the stock path (TestReview21Failover, TestReview21ZstdRetry) and in TestProvimuxSentTheRule.
			want := ""
			t.Logf("%s: accepted requests %v; entry=%q", kind, received, got)
			if got != want {
				t.Errorf("want %q, got %q", want, got)
			}
		})
	}
}
func TestReview21FirstCommit(t *testing.T) {
	h := newSentHarness(t, reviewRouter+"requests:\n  streaming:\n    keepalive-seconds: 1\n    send-keepalive-before-ft: true\n")
	useAnsweredBook(t, newSentClock())
	a := sentKeyFor(t, h, "review-public")
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	h.stand.status = func(*http.Request, int) (int, http.Header) { close(entered); <-release; return 0, nil }
	tick := make(chan time.Time, 1)
	stop := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} })
	defer stop()
	done := make(chan *http.Response, 1)
	errs := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest("POST", h.server.URL+"/v1/chat/completions", strings.NewReader(chatBody("review-public", true, "")))
		req.Header.Set("Authorization", "Bearer "+sentClientKey)
		req.Header.Set("Content-Type", "application/json")
		resp, e := h.server.Client().Do(req)
		if e != nil {
			errs <- e
			return
		}
		done <- resp
	}()
	<-entered
	tick <- time.Now()
	var resp *http.Response
	select {
	case resp = <-done:
	case e := <-errs:
		t.Fatal(e)
	case <-time.After(2 * time.Second):
		t.Fatal("fixture heartbeat never flushed")
	}
	if e := resp.Header.Get(logging.ProvimuxEntryHeader); e != "" {
		t.Fatal("fixture early entry", e)
	}
	if resp.Header.Get(logging.ProvimuxAccountHeader) != "" {
		t.Fatal("fixture early marker")
	}
	close(release)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(b, []byte("provimux-test-answer")) {
		t.Fatalf("fixture answer %q", b)
	}
	client, mgmt, _ := h.listsRow(sentClientKey, "review-public", a.ProvimuxAccountMarker())
	if client == nil || mgmt == nil {
		t.Fatal("fixture rows")
	}
	t.Logf("committed entry absent; after body client=%v management=%v", client.items(), mgmt.items())
	if len(client.items()) != 0 || len(mgmt.items()) != 0 {
		t.Error("a response with no committed marker or entry created history")
	}
}
func TestReview21FrozenAndSchemaCapture(t *testing.T) {
	cfg := sentMatrixConfig + `  interactions:
    - name: provimux-test-interactions
      models:
        - name: provimux-test-interactions-sent
          alias: provimux-test-interactions
      keys:
        - api-key: fixture-interactions
`
	h := newSentHarness(t, cfg)
	useAnsweredBook(t, newSentClock())
	sentLogins(t, h)
	sentCopilot(t, h, "provimux-test-copilot") // ported: a Copilot login the gateway's own login wrote (rule 4)
	var replayDir string
	if parent := os.Getenv("FIG21_TRIPLES"); parent != "" {
		var e error
		replayDir, e = os.MkdirTemp(parent, "run-")
		if e != nil {
			t.Fatal(e)
		}
	}
	var statements []map[string]string
	for i, tc := range append(sentMatrix(), sentInteractionsCase) {
		t.Run(tc.name, func(t *testing.T) {
			var captured *http.Request
			var raw []byte
			reviewSet(h, reviewRT(func(r *http.Request) (*http.Response, error) {
				body, e := io.ReadAll(r.Body)
				if e != nil {
					return nil, e
				}
				r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(body))
				if standInModel(t, r, body) != "" {
					captured = r.Clone(context.Background())
					raw = bytes.Clone(body)
				}
				return h.stand.RoundTrip(r)
			}))
			for j := 0; j < 3; j++ {
				logging.SetProvimuxAnsweredBy(j == 2)
				a := h.post(tc.path, tc.body, nil)
				if a.status != 200 || captured == nil {
					t.Fatalf("capture %d", a.status)
				}
				var out bytes.Buffer
				p := captured.Clone(context.Background())
				p.Body = io.NopCloser(bytes.NewReader(raw))
				if err := p.Write(&out); err != nil {
					t.Fatal(err)
				}
				for _, dir := range []string{replayDir, os.Getenv("FIG21_CAPTURE")} {
					if dir != "" {
						if e := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d-%d", i, j)), out.Bytes(), 0600); e != nil {
							t.Fatal(e)
						}
					}
				}
			}
			var frozen [][]byte
			for _, on := range []bool{false, true} {
				logging.SetProvimuxAnsweredBy(on)
				c := ex.WithUpstreamAttemptTracker(context.Background())
				r := captured.Clone(c)
				r.Body = io.NopCloser(bytes.NewReader(raw))
				r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
				rec := reviewRT(func(p *http.Request) (*http.Response, error) {
					var out bytes.Buffer
					if err := p.Write(&out); err != nil {
						return nil, err
					}
					frozen = append(frozen, out.Bytes())
					return reviewOK(p, "{}", "application/json"), nil
				})
				resp, e := httptransport.Decorate(c, &http.Client{Transport: rec}).Do(r)
				if e != nil {
					t.Fatal(e)
				}
				resp.Body.Close()
			}
			if len(frozen) != 2 || !bytes.Equal(frozen[0], frozen[1]) {
				t.Fatal("different serialized bytes for identical input")
			}
			a := h.post(tc.path, tc.body, nil)
			if a.status != 200 {
				t.Fatal(a.status)
			}
			statement := map[string]string{}
			for _, name := range []string{"account", "model", "model-basis", "model-sent", "effort", "entry"} {
				if value := a.get("X-Provimux-" + name); value != "" {
					plain, e := url.PathUnescape(value)
					if e != nil {
						t.Fatal(e)
					}
					statement["provimux_"+strings.ReplaceAll(name, "-", "_")] = plain
				}
			}
			if statement["provimux_account"] == "" {
				t.Fatal("missing statement on matrix response")
			}
			statements = append(statements, statement)
		})
	}
	if dir := os.Getenv("FIG21_CAPTURE"); dir != "" {
		raw, e := json.Marshal(statements)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "statements.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
		os.WriteFile(filepath.Join(dir, "client.json"), reviewGet(t, h, "/v1/models?include=accounts", sentClientKey), 0600)
		os.WriteFile(filepath.Join(dir, "management.json"), reviewGet(t, h, "/v8/management/provimux/model-routes", namesManagementKey), 0600)
	}
}
