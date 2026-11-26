package provimux

// Provimux: what a stop does to requests in flight, end to end. Provimux only chooses the number of seconds
// upstream's own stop (api.Server.Stop) is given (server.restart-drain-seconds, 25 by default); everything else
// is upstream's. Each case builds the real gateway (cmd/server), starts it on a free port of 127.0.0.1 in front
// of a stand-in vendor (an OpenAI-compatible key on 127.0.0.1), sends the process a real SIGTERM while the
// vendor holds an answer, and watches what the client gets and when the process exits. No real provider.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const (
	drainClientKey = "provimux-test-client-1"
	drainModel     = "provimux-test-drain-model"
)

// drainGatewayCount numbers the gateways of this test binary: each serves its own model alias, so a test's
// readiness check and requests can only be answered by its own process, even when two gateways of parallel tests
// picked the same free port and one of them failed to bind.
var drainGatewayCount atomic.Int64

var (
	drainBinaryOnce sync.Once
	drainBinaryPath string
	drainBinaryErr  error
)

// drainBinary builds cmd/server once per test binary.
func drainBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is a Unix signal")
	}
	drainBinaryOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(file), "..", "..")
		dir, err := os.MkdirTemp("", "provimux-drain-bin-")
		if err != nil {
			drainBinaryErr = err
			return
		}
		drainBinaryPath = filepath.Join(dir, "provimux")
		goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
		if _, errStat := os.Stat(goTool); runtime.GOROOT() == "" || errStat != nil {
			if goTool, err = exec.LookPath("go"); err != nil {
				drainBinaryErr = err
				return
			}
		}
		cmd := exec.Command(goTool, "build", "-o", drainBinaryPath, "./cmd/server")
		cmd.Dir = root
		if out, errBuild := cmd.CombinedOutput(); errBuild != nil {
			drainBinaryErr = fmt.Errorf("go build ./cmd/server: %v\n%s", errBuild, out)
		}
	})
	if drainBinaryErr != nil {
		t.Fatal(drainBinaryErr)
	}
	return drainBinaryPath
}

// drainVendor is the stand-in vendor: every answer sends its first part at once (a streamed answer its first
// chunk) and the rest when the test releases it.
type drainVendor struct {
	*httptest.Server
	release chan struct{}
	once    sync.Once
}

func newDrainVendor(t *testing.T) *drainVendor {
	v := &drainVendor{release: make(chan struct{})}
	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			select {
			case <-v.release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"the whole plain answer."},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunk := func(delta, finish string) {
			fin := "null"
			if finish != "" {
				fin = `"` + finish + `"`
			}
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}]}\n\n", delta, fin)
			flusher.Flush()
		}
		chunk(`{"role":"assistant","content":"first half, "}`, "")
		select {
		case <-v.release:
		case <-r.Context().Done():
			return
		}
		chunk(`{"content":"second half."}`, "")
		chunk(`{}`, "stop")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(func() { v.releaseAll(); v.Close() })
	return v
}

func (v *drainVendor) releaseAll() { v.once.Do(func() { close(v.release) }) }

// releaseAfter releases every held answer d after since.
func (v *drainVendor) releaseAfter(since time.Time, d time.Duration) {
	go func() {
		time.Sleep(time.Until(since.Add(d)))
		v.releaseAll()
	}()
}

// drainGateway is one gateway process.
type drainGateway struct {
	t      *testing.T
	cmd    *exec.Cmd
	port   int
	base   string
	model  string
	logBuf *lockedBuffer
	exited chan struct{}
	exitAt time.Time
	state  *os.ProcessState
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// startDrainGateway starts the gateway on a free port with serverLines inserted under server: of its config, and
// managementLines under management:. When another process took the port first, it starts it again on another.
func startDrainGateway(t *testing.T, vendorURL string, serverLines string, managementLines ...string) *drainGateway {
	t.Helper()
	for attempt := 0; ; attempt++ {
		g, ready := tryDrainGateway(t, freePort(t), vendorURL, serverLines, managementLines...)
		if ready {
			return g
		}
		if attempt == 4 || !strings.Contains(g.logBuf.String(), "address already in use") {
			t.Fatalf("gateway did not start:\n%s", g.logBuf.String())
		}
	}
}

// tryDrainGateway starts one gateway on port and reports whether it is ready: its own process is alive and its
// own model alias (no other gateway serves it) is listed on port. A gateway that is not ready is stopped.
func tryDrainGateway(t *testing.T, port int, vendorURL string, serverLines string, managementLines ...string) (*drainGateway, bool) {
	t.Helper()
	bin := drainBinary(t)
	dir := t.TempDir()
	model := fmt.Sprintf("%s-%d-%d", drainModel, os.Getpid(), drainGatewayCount.Add(1))
	cfg := fmt.Sprintf(`config-version: 8
models:
  live-lists: false
  published-lists: false
server:
  host: "127.0.0.1"
  port: %d
%smanagement:
  disable-control-panel: true
%saccess:
  api-keys:
    - %q
oauth:
  auth-dir: %q
api-keys:
  openai-compatibility:
    - name: "provimux-test-drain-vendor"
      base-url: %q
      fixed-models: true
      keys:
        - api-key: "provimux-test-vendor-key"
      models:
        - name: "provimux-test-upstream-model"
          alias: %q
`, port, serverLines, strings.Join(managementLines, ""), drainClientKey, filepath.Join(dir, "auths"), vendorURL+"/v1", model)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &drainGateway{t: t, port: port, model: model, base: fmt.Sprintf("http://127.0.0.1:%d", port), logBuf: &lockedBuffer{}, exited: make(chan struct{})}
	g.cmd = exec.Command(bin, "--config", path, "--local-model")
	g.cmd.Dir = dir
	g.cmd.Env = append(os.Environ(), "HOME="+dir, "MANAGEMENT_PASSWORD=")
	g.cmd.Stdout, g.cmd.Stderr = g.logBuf, g.logBuf
	if err := g.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = g.cmd.Wait()
		g.exitAt = time.Now()
		g.state = g.cmd.ProcessState
		close(g.exited)
	}()
	t.Cleanup(func() {
		_ = g.cmd.Process.Kill()
		<-g.exited
		if t.Failed() {
			t.Logf("gateway log:\n%s", g.logBuf.String())
		}
	})
	if g.waitReady(60 * time.Second) {
		return g, true
	}
	_ = g.cmd.Process.Kill()
	<-g.exited
	return g, false
}

func (g *drainGateway) waitReady(limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		select {
		case <-g.exited:
			return false
		default:
		}
		req, _ := http.NewRequest(http.MethodGet, g.base+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+drainClientKey)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"`+g.model+`"`) {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func (g *drainGateway) signal(sig syscall.Signal) time.Time {
	g.t.Helper()
	if err := g.cmd.Process.Signal(sig); err != nil {
		g.t.Fatalf("signal: %v", err)
	}
	return time.Now()
}

// waitExit waits for the process and returns how long after since it exited.
func (g *drainGateway) waitExit(since time.Time, limit time.Duration) time.Duration {
	g.t.Helper()
	select {
	case <-g.exited:
		return g.exitAt.Sub(since)
	case <-time.After(limit):
		_ = g.cmd.Process.Signal(syscall.SIGQUIT) // its goroutines go to the log below
		select {
		case <-g.exited:
		case <-time.After(2 * time.Second):
		}
		g.t.Fatalf("gateway still running %s after the signal", limit)
		return 0
	}
}

// drainAnswer is one answer seen by the client.
type drainAnswer struct {
	first chan struct{}
	done  chan struct{}
	mu    sync.Mutex
	text  strings.Builder
	err   error
	endAt time.Time
}

func (a *drainAnswer) body() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.text.String()
}

// whole: a streamed answer ended with its last chunk and [DONE]; a plain one is the vendor's whole answer.
func (a *drainAnswer) whole() bool {
	b := a.body()
	return strings.Contains(b, "second half.") && strings.Contains(b, "data: [DONE]") || strings.Contains(b, "the whole plain answer.")
}

// ask sends one chat request; a streamed one returns once its first chunk arrived, a plain one at once.
func (g *drainGateway) ask(t *testing.T, stream bool) *drainAnswer {
	t.Helper()
	a := &drainAnswer{first: make(chan struct{}), done: make(chan struct{})}
	body := fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"what is 1+1?"}]}`, g.model, stream)
	req, _ := http.NewRequest(http.MethodPost, g.base+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+drainClientKey)
	req.Header.Set("Content-Type", "application/json")
	var once sync.Once
	go func() {
		defer close(a.done)
		defer once.Do(func() { close(a.first) })
		finish := func(err error) {
			a.mu.Lock()
			a.err, a.endAt = err, time.Now()
			a.mu.Unlock()
		}
		resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
		if err != nil {
			finish(err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		reader := bufio.NewReader(resp.Body)
		for {
			line, errRead := reader.ReadString('\n')
			a.mu.Lock()
			a.text.WriteString(line)
			a.mu.Unlock()
			if strings.Contains(line, "first half") {
				once.Do(func() { close(a.first) })
			}
			if errRead != nil {
				if errors.Is(errRead, io.EOF) {
					errRead = nil
				}
				finish(errRead)
				return
			}
		}
	}()
	if stream {
		select {
		case <-a.first:
		case <-time.After(30 * time.Second):
			t.Fatal("no first chunk")
		}
		if !strings.Contains(a.body(), "first half") {
			t.Fatalf("stream ended before its first chunk: %v", a.err)
		}
	} else {
		time.Sleep(300 * time.Millisecond) // the request reaches the held vendor
	}
	return a
}

func (a *drainAnswer) wait(t *testing.T, limit time.Duration) {
	t.Helper()
	select {
	case <-a.done:
	case <-time.After(limit):
		t.Fatalf("answer still open after %s", limit)
	}
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.3f s", d.Seconds()) }

// No Provimux key and upstream's shutdown-timeout: 0, as every install copied it: an answer in flight, streamed
// or not, finishes after SIGTERM, and the process exits once it has, well inside the 25 s default.
func TestRestartDrainDefaultLetsAnswerFinish(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			t.Parallel()
			vendor := newDrainVendor(t)
			g := startDrainGateway(t, vendor.URL, "  shutdown-timeout: 0\n")
			answer := g.ask(t, stream)
			sent := g.signal(syscall.SIGTERM)
			vendor.releaseAfter(sent, 2*time.Second)
			answer.wait(t, 20*time.Second)
			if !answer.whole() || answer.err != nil {
				t.Fatalf("answer cut: err=%v\n%s", answer.err, answer.body())
			}
			took := g.waitExit(sent, 30*time.Second)
			if took < 2*time.Second || took > 10*time.Second {
				t.Fatalf("process exited %s after SIGTERM; the answer ended at %s", seconds(took), seconds(answer.endAt.Sub(sent)))
			}
			if code := g.state.ExitCode(); code != 0 {
				t.Fatalf("exit code %d", code)
			}
			if !strings.Contains(g.logBuf.String(), "[info ] [provimux_restart_drain.go:") ||
				!strings.Contains(g.logBuf.String(), "stopping: waiting up to 25 seconds for the answers in flight") {
				t.Fatal("no info line when the stop began to wait")
			}
			t.Logf("answer whole at %s, process exited at %s after SIGTERM", seconds(answer.endAt.Sub(sent)), seconds(took))
		})
	}
}

// heldUntilCut sends SIGTERM while the vendor holds a streamed answer it never finishes, and checks that the
// answer is cut and the process gone within [want-0.2 s, want+limitOver].
func heldUntilCut(t *testing.T, serverLines string, want, limitOver time.Duration) {
	t.Helper()
	vendor := newDrainVendor(t)
	g := startDrainGateway(t, vendor.URL, serverLines)
	answer := g.ask(t, true)
	sent := g.signal(syscall.SIGTERM)
	took := g.waitExit(sent, want+limitOver+5*time.Second)
	answer.wait(t, 5*time.Second)
	if answer.whole() {
		t.Fatalf("the held answer finished:\n%s", answer.body())
	}
	cut := answer.endAt.Sub(sent)
	if cut < want-200*time.Millisecond || cut > want+limitOver {
		t.Fatalf("answer cut at %s after SIGTERM, want %s", seconds(cut), want)
	}
	if took < cut-50*time.Millisecond || took > want+limitOver {
		t.Fatalf("process exited %s after SIGTERM, want by %s", seconds(took), want+limitOver)
	}
	t.Logf("answer cut at %s, process exited at %s after SIGTERM", seconds(cut), seconds(took))
}

// server.restart-drain-seconds: 0 cuts the answer at once, as before, whatever upstream's key says.
func TestRestartDrainProvimuxZeroCutsAtOnce(t *testing.T) {
	t.Parallel()
	heldUntilCut(t, "  shutdown-timeout: 20\n  restart-drain-seconds: 0\n", 0, 1500*time.Millisecond)
}

// Provimux's key, when set, decides alone: 10 with upstream's 20 waits 10 s.
func TestRestartDrainProvimuxKeyDecides(t *testing.T) {
	t.Parallel()
	heldUntilCut(t, "  shutdown-timeout: 20\n  restart-drain-seconds: 10\n", 10*time.Second, 1500*time.Millisecond)
}

// No Provimux key: upstream's shutdown-timeout counts when it is larger than the default: 28 waits 28 s.
func TestRestartDrainUpstreamLargerCounts(t *testing.T) {
	t.Parallel()
	heldUntilCut(t, "  shutdown-timeout: 28\n", 28*time.Second, 1500*time.Millisecond)
}

// The longest wait: Provimux's key at its maximum, 30, which is also upstream's own bound on the whole stop.
// The answer is cut at 30 s and the process is gone right after.
func TestRestartDrainEndsWithinUpstreamBound(t *testing.T) {
	t.Parallel()
	heldUntilCut(t, "  restart-drain-seconds: 30\n", 30*time.Second, 1500*time.Millisecond)
}

// Upstream closes the listener when the stop begins: a new connection during the wait is refused (the client
// retries, or fails), while the answer in flight goes on.
func TestRestartDrainNewConnectionRefused(t *testing.T) {
	t.Parallel()
	vendor := newDrainVendor(t)
	g := startDrainGateway(t, vendor.URL, "")
	answer := g.ask(t, true)
	sent := g.signal(syscall.SIGTERM)
	time.Sleep(500 * time.Millisecond)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", g.port), 2*time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("a new connection was accepted during the wait")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("new connection: %v, want connection refused", err)
	}
	vendor.releaseAll()
	answer.wait(t, 10*time.Second)
	if !answer.whole() {
		t.Fatalf("answer cut:\n%s", answer.body())
	}
	took := g.waitExit(sent, 30*time.Second)
	t.Logf("new connection at 0.5 s: %v; answer whole at %s; process exited at %s", err, seconds(answer.endAt.Sub(sent)), seconds(took))
}

// A second SIGTERM during the wait changes nothing: the answer still finishes and the process exits after it.
func TestRestartDrainSecondSignalIgnored(t *testing.T) {
	t.Parallel()
	vendor := newDrainVendor(t)
	g := startDrainGateway(t, vendor.URL, "")
	answer := g.ask(t, true)
	sent := g.signal(syscall.SIGTERM)
	time.Sleep(500 * time.Millisecond)
	g.signal(syscall.SIGTERM)
	time.Sleep(500 * time.Millisecond)
	select {
	case <-g.exited:
		t.Fatalf("the second SIGTERM ended the process: %v", g.state)
	default:
	}
	vendor.releaseAll()
	answer.wait(t, 10*time.Second)
	if !answer.whole() {
		t.Fatalf("answer cut:\n%s", answer.body())
	}
	took := g.waitExit(sent, 30*time.Second)
	t.Logf("answer whole at %s; process exited at %s after the first SIGTERM", seconds(answer.endAt.Sub(sent)), seconds(took))
}

// wsTurn is one Responses WebSocket turn seen by the client.
type wsTurn struct {
	conn      *websocket.Conn
	completed bool
	closeErr  error
	endAt     time.Time
	done      chan struct{}
}

// openTurn dials /v1/responses, sends one turn, and returns once its first text arrived.
func (g *drainGateway) openTurn(t *testing.T) *wsTurn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(g.base, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer " + drainClientKey}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	turn := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"what is 1+1?"}]}`, g.model)
	if err = conn.WriteMessage(websocket.TextMessage, []byte(turn)); err != nil {
		t.Fatal(err)
	}
	w := &wsTurn{conn: conn, done: make(chan struct{})}
	first := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(w.done)
		defer once.Do(func() { close(first) })
		for {
			_, msg, errRead := conn.ReadMessage()
			if errRead != nil {
				w.closeErr, w.endAt = errRead, time.Now()
				return
			}
			if strings.Contains(string(msg), "first half") {
				once.Do(func() { close(first) })
			}
			if strings.Contains(string(msg), `"response.completed"`) {
				w.completed, w.endAt = true, time.Now()
				return
			}
		}
	}()
	select {
	case <-first:
	case <-time.After(30 * time.Second):
		t.Fatal("no answer text on the socket")
	}
	if w.closeErr != nil {
		t.Fatalf("socket closed before the answer: %v", w.closeErr)
	}
	return w
}

// A Responses WebSocket turn in flight is not part of upstream's wait (its connection is hijacked, so the HTTP
// server does not track it): when it is the only thing in flight, the stop does not wait for it and the
// process exits at once, cutting the turn without response.completed.
func TestRestartDrainWebsocketTurnNotWaitedFor(t *testing.T) {
	t.Parallel()
	vendor := newDrainVendor(t)
	g := startDrainGateway(t, vendor.URL, "")
	turn := g.openTurn(t)
	sent := g.signal(syscall.SIGTERM)
	took := g.waitExit(sent, 10*time.Second)
	select {
	case <-turn.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the socket stayed open after the process exited")
	}
	vendor.releaseAll()
	if turn.completed {
		t.Fatal("the turn completed")
	}
	if took > 2*time.Second {
		t.Fatalf("process exited %s after SIGTERM; it should not wait for the turn", seconds(took))
	}
	t.Logf("turn cut at %s (%v); process exited at %s after SIGTERM", seconds(turn.endAt.Sub(sent)), turn.closeErr, seconds(took))
}

// While an HTTP answer keeps the wait open, a WebSocket turn in flight goes on and can finish too; it is cut when
// the wait ends, with the process.
func TestRestartDrainWebsocketTurnDuringHTTPWait(t *testing.T) {
	t.Parallel()
	vendor := newDrainVendor(t)
	g := startDrainGateway(t, vendor.URL, "  restart-drain-seconds: 5\n")
	turn := g.openTurn(t)
	answer := g.ask(t, true)
	sent := g.signal(syscall.SIGTERM)
	vendor.releaseAfter(sent, time.Second)
	answer.wait(t, 10*time.Second)
	<-turn.done
	took := g.waitExit(sent, 15*time.Second)
	t.Logf("HTTP answer whole=%t at %s; WebSocket turn completed=%t at %s (%v); process exited at %s",
		answer.whole(), seconds(answer.endAt.Sub(sent)), turn.completed, seconds(turn.endAt.Sub(sent)), turn.closeErr, seconds(took))
	if !answer.whole() || !turn.completed {
		t.Fatal("an answer was cut")
	}
}

// An answer relayed through the AI Studio relay (/v1/ws) is cut when the stop begins: upstream stops the relay
// before its wait.
func TestRestartDrainAIStudioRelayAnswerCut(t *testing.T) {
	t.Parallel()
	vendor := newDrainVendor(t)
	g := startDrainGateway(t, vendor.URL, "")
	wsURL := "ws" + strings.TrimPrefix(g.base, "http") + "/v1/ws"
	relay, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer " + drainClientKey}})
	if err != nil {
		t.Fatalf("dial the relay: %v", err)
	}
	defer func() { _ = relay.Close() }()
	type relayMsg struct {
		ID      string         `json:"id"`
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload,omitempty"`
	}
	requests := make(chan relayMsg, 4)
	go func() {
		for {
			var m relayMsg
			if relay.ReadJSON(&m) != nil {
				close(requests)
				return
			}
			if m.Type == "http_request" {
				requests <- m
			}
		}
	}()
	var model string
	deadline := time.Now().Add(15 * time.Second)
	for model == "" && time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, g.base+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+drainClientKey)
		if resp, errGet := http.DefaultClient.Do(req); errGet == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			for _, id := range strings.Split(string(body), `"id":"`)[1:] {
				if name := id[:strings.Index(id, `"`)]; name != g.model {
					model = name
					break
				}
			}
		}
		if model == "" {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if model == "" {
		t.Fatal("the relay registered no model")
	}

	body := fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, model)
	req, _ := http.NewRequest(http.MethodPost, g.base+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+drainClientKey)
	req.Header.Set("Content-Type", "application/json")
	type answer struct {
		text  string
		err   error
		endAt time.Time
	}
	firstSeen, answered := make(chan struct{}), make(chan answer, 1)
	go func() {
		resp, errDo := http.DefaultClient.Do(req)
		if errDo != nil {
			answered <- answer{err: errDo, endAt: time.Now()}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		var all strings.Builder
		reader := bufio.NewReader(resp.Body)
		once := sync.Once{}
		for {
			line, errLine := reader.ReadString('\n')
			all.WriteString(line)
			if strings.Contains(line, "first marker") {
				once.Do(func() { close(firstSeen) })
			}
			if errLine != nil {
				answered <- answer{text: all.String(), err: errLine, endAt: time.Now()}
				return
			}
		}
	}()
	var request relayMsg
	select {
	case request = <-requests:
	case <-time.After(15 * time.Second):
		t.Fatal("the relay got no request")
	}
	send := func(kind string, payload map[string]any) error {
		return relay.WriteJSON(relayMsg{ID: request.ID, Type: kind, Payload: payload})
	}
	chunk := func(text, finish string) map[string]any {
		fin := ""
		if finish != "" {
			fin = `,"finishReason":"` + finish + `"`
		}
		return map[string]any{"data": `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"` + text + `"}]},"index":0` + fin + `}]}` + "\n\n"}
	}
	_ = send("stream_start", map[string]any{"status": 200, "headers": map[string]any{"Content-Type": []string{"text/event-stream"}}})
	_ = send("stream_chunk", chunk("first marker", ""))
	select {
	case <-firstSeen:
	case <-time.After(15 * time.Second):
		t.Fatal("the client got no first chunk")
	}
	sent := g.signal(syscall.SIGTERM)
	time.Sleep(150 * time.Millisecond)
	errLast := send("stream_chunk", chunk("last marker", "STOP"))
	_ = send("stream_end", nil)
	var got answer
	select {
	case got = <-answered:
	case <-time.After(10 * time.Second):
		t.Fatal("the answer did not end")
	}
	took := g.waitExit(sent, 30*time.Second)
	t.Logf("relay answer ended at %s (err %v, relay write of the last chunk: %v); process exited at %s; client got:\n%s",
		seconds(got.endAt.Sub(sent)), got.err, errLast, seconds(took), got.text)
	if strings.Contains(got.text, "last marker") {
		t.Fatal("the relayed answer finished")
	}
}

// The console's path: server.restart-drain-seconds written through the generic config path
// (/v8/management/config/server/restart-drain-seconds) is the number the running gateway's next stop uses,
// once the write has been reloaded. Written 0, a held answer is cut at once; written 3, at 3 s; written 31, which
// is saved and read back as 31, at 30 s, and a save of another key with 31 in the file works.
func TestRestartDrainKeyWrittenThroughConfigPath(t *testing.T) {
	const secret = "provimux-test-management-secret"
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{{"0", 0}, {"3", 3 * time.Second}, {"31", 30 * time.Second}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			vendor := newDrainVendor(t)
			g := startDrainGateway(t, vendor.URL, "  shutdown-timeout: 0\n", fmt.Sprintf("  secret-key: %q\n", secret))
			callKey := func(method, key, body string) (int, string) {
				t.Helper()
				req, _ := http.NewRequest(method, g.base+"/v8/management/config/server/"+key, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+secret)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				b, _ := io.ReadAll(resp.Body)
				return resp.StatusCode, string(b)
			}
			call := func(method, body string) (int, string) {
				t.Helper()
				return callKey(method, "restart-drain-seconds", body)
			}
			// Upstream applies a management write through its file watcher, which starts a moment after the
			// server answers; a write before that is applied only with the next change.
			for deadline := time.Now().Add(20 * time.Second); !strings.Contains(g.logBuf.String(), "file watcher started"); {
				if time.Now().After(deadline) {
					t.Fatal("the file watcher did not start")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if code, body := call(http.MethodGet, ""); code != http.StatusNotFound {
				t.Fatalf("GET of the unset key: %d %s", code, body)
			}
			if code, body := call(http.MethodPut, "2.5"); code != http.StatusUnprocessableEntity {
				t.Fatalf("PUT 2.5: %d %s", code, body)
			}
			if code, body := call(http.MethodPut, tc.value); code != http.StatusOK {
				t.Fatalf("PUT %s: %d %s", tc.value, code, body)
			}
			if code, body := call(http.MethodGet, ""); code != http.StatusOK || strings.TrimSpace(body) != tc.value {
				t.Fatalf("GET after PUT: %d %s", code, body)
			}
			if tc.value == "31" {
				if code, body := callKey(http.MethodPut, "shutdown-timeout", "4"); code != http.StatusOK {
					t.Fatalf("PUT of another key with 31 in the file: %d %s", code, body)
				}
				if code, body := call(http.MethodGet, ""); code != http.StatusOK || strings.TrimSpace(body) != "31" {
					t.Fatalf("GET after another key's save: %d %s", code, body)
				}
			}
			time.Sleep(time.Second) // the write is reloaded asynchronously, as every management write is
			answer := g.ask(t, true)
			sent := g.signal(syscall.SIGTERM)
			took := g.waitExit(sent, tc.want+10*time.Second)
			answer.wait(t, 5*time.Second)
			cut := answer.endAt.Sub(sent)
			if answer.whole() || cut < tc.want-200*time.Millisecond || cut > tc.want+1500*time.Millisecond {
				t.Fatalf("answer whole=%t, ended at %s, want cut at %s", answer.whole(), seconds(cut), tc.want)
			}
			t.Logf("written %s: answer cut at %s, process exited at %s after SIGTERM", tc.value, seconds(cut), seconds(took))
		})
	}
}

// A whole number out of range never keeps the gateway from starting: it is used as the nearest bound (0 or 30)
// with one warning line. -5 is used as 0: a held answer is cut at once, and no wait is announced.
func TestRestartDrainOutOfRangeStarts(t *testing.T) {
	for _, tc := range []struct {
		value string
		used  int
	}{{"-5", 0}, {"99", 30}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			vendor := newDrainVendor(t)
			g := startDrainGateway(t, vendor.URL, "  restart-drain-seconds: "+tc.value+"\n")
			warning := fmt.Sprintf("server.restart-drain-seconds is %s; using %d (it takes 0 to 30)", tc.value, tc.used)
			if n := strings.Count(g.logBuf.String(), warning); n != 1 {
				t.Fatalf("%d warning lines %q", n, warning)
			}
			if tc.used != 0 {
				return
			}
			answer := g.ask(t, true)
			sent := g.signal(syscall.SIGTERM)
			took := g.waitExit(sent, 10*time.Second)
			answer.wait(t, 5*time.Second)
			if answer.whole() || took > 1500*time.Millisecond {
				t.Fatalf("answer whole=%t, process exited at %s", answer.whole(), seconds(took))
			}
			if strings.Contains(g.logBuf.String(), "stopping: waiting") {
				t.Fatal("a wait was announced with a budget of 0")
			}
			t.Logf("%s used as %d: answer cut at %s, process exited at %s", tc.value, tc.used, seconds(answer.endAt.Sub(sent)), seconds(took))
		})
	}
}

// Readiness proves the test's own process: a gateway started on a port another gateway already serves is not
// ready, even though the port answers /v1/models, and its log says why.
func TestRestartDrainReadinessIsOwnProcess(t *testing.T) {
	t.Parallel()
	vendor := newDrainVendor(t)
	first := startDrainGateway(t, vendor.URL, "")
	second, ready := tryDrainGateway(t, first.port, vendor.URL, "")
	if ready {
		t.Fatal("a gateway that could not bind was taken as ready")
	}
	if !strings.Contains(second.logBuf.String(), "address already in use") {
		t.Fatalf("second gateway's log:\n%s", second.logBuf.String())
	}
	if !first.waitReady(time.Second) {
		t.Fatal("the first gateway stopped serving")
	}
}
