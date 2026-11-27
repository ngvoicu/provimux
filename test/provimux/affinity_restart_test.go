package provimux

// Session-affinity bindings survive a restart of the gateway process (Provimux layer,
// routing.session-affinity-persist; sdk/cliproxy/provimux_affinity_persist.go). The gateway runs in
// a child process (this test binary, re-run as TestProvimuxAffinityGatewayProcess) on loopback, in
// front of a stand-in OpenAI-compatible vendor with two API keys under one model name. The child is
// stopped with SIGTERM (it writes the bindings when it stops), or killed with SIGKILL after a
// periodic snapshot (twice, the second time before any new write), and started again on the same
// config and auth folder; each case with routing.cooldown.save-cooldown-status off and on.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
)

const (
	affinityGatewayEnv = "PROVIMUX_TEST_AFFINITY_GATEWAY_CONFIG"
	affinityModel      = "provimux-test-affinity-model"
	affinityClient1    = "provimux-test-client-1"
	affinityClient2    = "provimux-test-client-2"
	affinityInterval   = "PROVIMUX_TEST_AFFINITY_SNAPSHOT_INTERVAL"
	// affinityStall: while the file it names exists, every snapshot write of the gateway waits.
	affinityStall = "PROVIMUX_TEST_AFFINITY_STALL"
	// affinityLinger: while the file it names exists, the gateway process stays after its stop.
	affinityLinger = "PROVIMUX_TEST_AFFINITY_LINGER"
	// affinityHome: the gateway runs in Home mode, its Home server a closed loopback port (main sets
	// cfg.Home from the Home control plane; here the test sets it, nothing answers there).
	affinityHome = "PROVIMUX_TEST_AFFINITY_HOME_PORT"
	// affinityManagement is the gateway's management key (MANAGEMENT_PASSWORD), for the state.
	affinityManagement = "provimux-test-management"
)

func affinityWaitWhile(path string) {
	for {
		if _, err := os.Stat(path); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestProvimuxAffinityGatewayProcess is the gateway process of TestProvimuxAffinitySurvivesRestart.
func TestProvimuxAffinityGatewayProcess(t *testing.T) {
	path := os.Getenv(affinityGatewayEnv)
	if path == "" {
		t.Skip("the gateway process of TestProvimuxAffinitySurvivesRestart")
	}
	registry.SetLocalModelCatalogs(true)
	if interval, errParse := time.ParseDuration(os.Getenv(affinityInterval)); errParse == nil {
		cliproxy.SetProvimuxAffinitySnapshotInterval(interval)
	}
	// The start's deadline is not what these tests are about: a loaded machine must not trip it.
	cliproxy.SetProvimuxAffinityStartDeadline(time.Minute)
	if stall := os.Getenv(affinityStall); stall != "" {
		cliproxy.SetProvimuxAffinityBeforeWrite(func() { affinityWaitWhile(stall) })
	}
	togetherGatewayHooks() // a second try held in its wait (retry_route_restart_test.go)
	if linger := os.Getenv(affinityLinger); linger != "" {
		defer affinityWaitWhile(linger)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if port, errPort := strconv.Atoi(os.Getenv(affinityHome)); errPort == nil {
		cfg.Home = config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: port, DisableClusterDiscovery: true}
	}
	service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(path).Build()
	if err != nil {
		t.Fatal(err)
	}
	util.SetLogLevel(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	if errRun := service.Run(ctx); errRun != nil && !errors.Is(errRun, context.Canceled) {
		t.Fatal(errRun)
	}
}

// affinityVendor is the stand-in vendor: it answers chat completions and records which key asked. A request whose
// body holds "[hold]" is announced on held (with its key) and answered only once release is closed.
type affinityVendor struct {
	mu      sync.Mutex
	last    string
	held    chan string
	release chan struct{}
}

func (v *affinityVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	v.mu.Lock()
	v.last = key
	held, release := v.held, v.release
	v.mu.Unlock()
	if held != nil && bytes.Contains(body, []byte("[hold]")) {
		held <- key
		<-release
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"answer from `+key+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

type affinityOptions struct {
	unset        bool // routing.session-affinity-persist not in the file (the default)
	persist      bool // routing.session-affinity-persist
	clientKeys   bool // access.api-keys set (requests carry a caller scope)
	saveCooldown bool // routing.cooldown.save-cooldown-status
	kvDisabled   bool // kv-store.disabled
	affinityOff  bool // routing.session-affinity: false
	drain        int  // server.restart-drain-seconds (0: not in the file; a test binary's default is then 0)
	home         bool // Home mode, the Home server a closed loopback port
}

type affinityGateway struct {
	t       *testing.T
	dir     string
	config  string
	port    int
	cmd     *exec.Cmd
	logMu   sync.Mutex
	log     bytes.Buffer
	vendor  *affinityVendor
	stopped chan struct{}
	home    bool
	logFrom int // where the running process's log begins
}

func newAffinityGateway(t *testing.T, o affinityOptions) *affinityGateway {
	t.Helper()
	vendor := &affinityVendor{}
	upstream := httptest.NewServer(vendor)
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	g := &affinityGateway{t: t, dir: dir, config: filepath.Join(dir, "config.yaml"), port: freePort(t), vendor: vendor, home: o.home}
	access := fmt.Sprintf("access:\n  api-keys: [%q, %q]\n", affinityClient1, affinityClient2)
	if !o.clientKeys {
		access = ""
	}
	drain := ""
	if o.drain > 0 {
		drain = fmt.Sprintf("  restart-drain-seconds: %d\n", o.drain)
	}
	persist := fmt.Sprintf("  session-affinity-persist: %t\n", o.persist)
	if o.unset {
		persist = ""
	}
	yaml := fmt.Sprintf(`config-version: 8
models:
  live-lists: false
  published-lists: false
server:
  host: "127.0.0.1"
  port: %d
%s%srouting:
  strategy: "round-robin"
  session-affinity: %t
%s  cooldown:
    save-cooldown-status: %t
kv-store:
  disabled: %t
oauth:
  auth-dir: %q
observability:
  logs:
    debug: true
api-keys:
  openai-compatibility:
    - name: "provimux-test-vendor"
      base-url: %q
      fixed-models: true
      keys:
        - api-key: "provimux-test-key-a"
        - api-key: "provimux-test-key-b"
      models:
        - name: "provimux-test-upstream"
          alias: %q
`, g.port, drain, access, !o.affinityOff, persist, o.saveCooldown, o.kvDisabled, authDir, upstream.URL+"/v1", affinityModel)
	if err := os.WriteFile(g.config, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.stop)
	return g
}

func (g *affinityGateway) Write(p []byte) (int, error) {
	g.logMu.Lock()
	defer g.logMu.Unlock()
	return g.log.Write(p)
}

func (g *affinityGateway) snapshot() string {
	return filepath.Join(g.dir, "auths", "kv.db.affinity")
}

func (g *affinityGateway) key() string {
	return filepath.Join(g.dir, "auths", "kv.db.affinity-key")
}

// start runs the gateway with a periodic snapshot every 200 ms.
func (g *affinityGateway) start() { g.startEvery("200ms") }

// startEvery runs the gateway with the given snapshot interval.
func (g *affinityGateway) startEvery(interval string, env ...string) {
	g.t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProvimuxAffinityGatewayProcess$", "-test.v")
	// Nothing leaves the machine: the version and console updaters a start runs find no proxy;
	// loopback (the stand-in vendor) is never proxied.
	cmd.Env = append(os.Environ(), affinityGatewayEnv+"="+g.config, "HOME="+g.dir, affinityInterval+"="+interval,
		"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=", "MANAGEMENT_PASSWORD="+affinityManagement)
	if g.home {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", affinityHome, freePort(g.t)))
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.Dir = g.dir
	cmd.Stdout = g
	cmd.Stderr = g
	g.logMu.Lock()
	g.logFrom = g.log.Len()
	g.logMu.Unlock()
	if err := cmd.Start(); err != nil {
		g.t.Fatal(err)
	}
	g.cmd = cmd
	g.stopped = make(chan struct{})
	go func(c *exec.Cmd, done chan struct{}) { _ = c.Wait(); close(done) }(cmd, g.stopped)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-g.stopped:
			g.t.Fatalf("gateway exited at start:\n%s", g.log.String())
		default:
		}
		if g.home {
			// Home mode serves the models Home gives it (none here) and no management API: any
			// answer says it serves (the bindings' start ran before the server was made).
			if _, err := g.stateOnce(); err != nil && strings.Contains(err.Error(), "status ") {
				return
			}
		} else if code, _ := g.models(); code == http.StatusOK {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	g.t.Fatalf("gateway not ready:\n%s", g.log.String())
}

func (g *affinityGateway) models() (int, error) {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v1/models", g.port), nil)
	req.Header.Set("Authorization", "Bearer "+affinityClient1)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(affinityModel)) {
		return 0, nil
	}
	return resp.StatusCode, nil
}

// stop sends SIGTERM, as launchd's bootout does, and waits for the process to end.
func (g *affinityGateway) stop() {
	if g.cmd == nil || g.cmd.Process == nil {
		return
	}
	_ = g.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-g.stopped:
	case <-time.After(20 * time.Second):
		_ = g.cmd.Process.Kill()
		<-g.stopped
	}
	g.cmd = nil
}

// restart stops the gateway with SIGTERM, or, with kill, waits for a periodic snapshot written
// after the last request and kills it with SIGKILL, then starts it again. With twice (and kill),
// the started gateway (snapshot interval one hour) is killed again at once, before it writes
// anything, and started again.
func (g *affinityGateway) restart(kill, twice bool) {
	g.t.Helper()
	if !kill {
		g.stop()
		g.start()
		return
	}
	after := time.Now()
	deadline := after.Add(10 * time.Second)
	for {
		if info, err := os.Stat(g.snapshot()); err == nil && info.ModTime().After(after) {
			break
		}
		if time.Now().After(deadline) {
			g.t.Fatalf("no periodic snapshot:\n%s", g.log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	g.kill()
	if !twice {
		g.start()
		return
	}
	written, _ := os.ReadFile(g.snapshot())
	g.startEvery("1h")
	g.kill()
	if now, _ := os.ReadFile(g.snapshot()); !bytes.Equal(now, written) {
		g.t.Fatal("premise: the snapshot changed between the two crashes")
	}
	g.startEvery("1h")
}

func (g *affinityGateway) kill() {
	_ = g.cmd.Process.Kill()
	<-g.stopped
	g.cmd = nil
}

type affinityConversation struct {
	client    string
	sessionID string // sent as Pi does (X-Client-Request-Id and prompt_cache_key); empty: none
	messages  []map[string]string
}

// turn sends the next user message with the whole history and returns the key that answered.
func (g *affinityGateway) turn(c *affinityConversation, text string) string {
	g.t.Helper()
	c.messages = append(c.messages, map[string]string{"role": "user", "content": text})
	payload := map[string]any{"model": affinityModel, "messages": append([]map[string]string{{"role": "system", "content": "You are the provimux-test helper."}}, c.messages...)}
	if c.sessionID != "" {
		payload["prompt_cache_key"] = c.sessionID
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", g.port), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.client)
	req.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		req.Header.Set("X-Client-Request-Id", c.sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		g.t.Fatalf("status %d: %s", resp.StatusCode, answer)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errDecode := json.Unmarshal(answer, &parsed); errDecode != nil || len(parsed.Choices) == 0 {
		g.t.Fatalf("answer %s", answer)
	}
	reply := parsed.Choices[0].Message.Content
	c.messages = append(c.messages, map[string]string{"role": "assistant", "content": reply})
	g.vendor.mu.Lock()
	defer g.vendor.mu.Unlock()
	return g.vendor.last
}

// otherNext sends new conversations until one lands on key, so that the next new binding goes to
// the other key: a conversation that comes back to key after that did not get there by turn.
func (g *affinityGateway) otherNext(key string, probes *int) {
	g.t.Helper()
	for i := 0; i < 3; i++ {
		*probes++
		probe := &affinityConversation{client: affinityClient1}
		if g.turn(probe, fmt.Sprintf("probe %d: what is %d+%d?", *probes, *probes, *probes)) == key {
			return
		}
	}
	g.t.Fatalf("no probe landed on %s", key)
}

func TestProvimuxAffinitySurvivesRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stops the gateway with SIGTERM")
	}
	if os.Getenv(affinityGatewayEnv) != "" {
		t.Skip("inside the gateway process")
	}
	type expectation struct{ withID, withoutID bool } // true: back on its key after the restart
	for _, saveCooldown := range []bool{false, true} {
		for _, tc := range []struct {
			name        string
			options     affinityOptions
			kill, twice bool
			want        expectation
		}{
			{"SIGTERM", affinityOptions{persist: true, clientKeys: true}, false, false, expectation{true, false}},
			{"SIGKILL after a periodic snapshot", affinityOptions{persist: true, clientKeys: true}, true, false, expectation{true, false}},
			{"SIGKILL twice, the second before any new write", affinityOptions{persist: true, clientKeys: true}, true, true, expectation{true, false}},
			{"SIGTERM, no client keys", affinityOptions{persist: true}, false, false, expectation{true, true}},
			{"session-affinity-persist false: today's behaviour", affinityOptions{clientKeys: true}, false, false, expectation{false, false}},
			{"session-affinity-persist not set: on by default", affinityOptions{unset: true, clientKeys: true}, false, false, expectation{true, false}},
			{"kv-store disabled: today's behaviour, no file", affinityOptions{persist: true, clientKeys: true, kvDisabled: true}, false, false, expectation{false, false}},
		} {
			tc.options.saveCooldown = saveCooldown
			t.Run(fmt.Sprintf("%s, save-cooldown-status %v", tc.name, saveCooldown), func(t *testing.T) {
				g := newAffinityGateway(t, tc.options)
				g.start()
				withID := &affinityConversation{client: affinityClient1, sessionID: "provimux-test-session-1"}
				withoutID := &affinityConversation{client: affinityClient1}
				names := map[*affinityConversation]string{withID: "with id", withoutID: "without id"}
				bound := map[*affinityConversation]string{}
				for _, c := range []*affinityConversation{withID, withoutID} {
					for i := 1; i <= 3; i++ {
						key := g.turn(c, fmt.Sprintf("[%s] question %d: what is %d+%d?", names[c], i, i, i))
						if i == 1 {
							bound[c] = key
						} else if key != bound[c] {
							t.Fatalf("%s: turn %d on %s, bound to %s", names[c], i, key, bound[c])
						}
					}
				}
				g.restart(tc.kill, tc.twice)
				kept := (tc.options.persist || tc.options.unset) && !tc.options.kvDisabled
				if _, err := os.Stat(g.snapshot()); kept && err != nil {
					t.Errorf("the snapshot is gone after the start (%v): it stays until the next write", err)
				}

				probes := 0
				if tc.options.clientKeys {
					// A conversation of client 1, re-sent by client 2: never client 1's binding.
					g.otherNext(bound[withoutID], &probes)
					copied := &affinityConversation{client: affinityClient2, messages: append([]map[string]string(nil), withoutID.messages...)}
					if key := g.turn(copied, "[copied] question 4: what is 4+4?"); key == bound[withoutID] {
						t.Errorf("client 2 got client 1's binding %s", key)
					}
				}
				for _, c := range []*affinityConversation{withID, withoutID} {
					want := tc.want.withID
					if c == withoutID {
						want = tc.want.withoutID
					}
					g.otherNext(bound[c], &probes)
					key := g.turn(c, "question 4 after the restart: what is 4+4?")
					if want && key != bound[c] {
						t.Errorf("%s: bound to %s, came back on %s", names[c], bound[c], key)
					}
					if !want && key == bound[c] {
						t.Errorf("%s: came back on %s: expected a new binding, the next key in turn", names[c], key)
					}
				}
				if !kept {
					g.stop()
					for _, path := range []string{g.snapshot(), g.key()} {
						if _, err := os.Stat(path); !os.IsNotExist(err) {
							t.Errorf("%s was written (%v)", filepath.Base(path), err)
						}
					}
				}
				if t.Failed() {
					scanner := bufio.NewScanner(strings.NewReader(g.log.String()))
					for scanner.Scan() {
						if line := scanner.Text(); strings.Contains(line, "session-affinity") || strings.Contains(line, "session affinity") {
							t.Log(line)
						}
					}
				}
			})
		}
	}
}

// Case 4 of the third review across processes: the old gateway's periodic write is stalled when
// it is told to stop; its stop gives up at the deadline and the process lingers. The new gateway
// starts meanwhile, restores the last file written, and writes nothing while the old one holds the
// store. When the old write is released it never lands; when the old process ends, the new one
// writes. A conversation only the stalled write held never comes back; one the new process made
// does.
func TestProvimuxAffinityStalledWriterAcrossProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stops the gateway with SIGTERM")
	}
	if os.Getenv(affinityGatewayEnv) != "" {
		t.Skip("inside the gateway process")
	}
	g := newAffinityGateway(t, affinityOptions{persist: true, clientKeys: true})
	stall, linger := filepath.Join(g.dir, "stall"), filepath.Join(g.dir, "linger")
	g.startEvery("200ms", affinityStall+"="+stall, affinityLinger+"="+linger)
	kept := &affinityConversation{client: affinityClient1, sessionID: "provimux-test-kept"}
	keptKey := g.turn(kept, "kept: what is 1+1?")
	// A periodic write after that turn holds it.
	after := time.Now()
	for {
		if info, err := os.Stat(g.snapshot()); err == nil && info.ModTime().After(after) {
			break
		}
		if time.Since(after) > 10*time.Second {
			t.Fatal("no periodic snapshot")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, path := range []string{stall, linger} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stalledOnly := &affinityConversation{client: affinityClient1, sessionID: "provimux-test-stalled-only"}
	stalledKey := g.turn(stalledOnly, "stalled only: what is 2+2?")
	time.Sleep(500 * time.Millisecond) // the next periodic write starts and stalls
	old := g.cmd
	oldStopped := g.stopped
	_ = old.Process.Signal(syscall.SIGTERM)
	time.Sleep(2500 * time.Millisecond) // past the stop's deadline: the old process lingers
	select {
	case <-oldStopped:
		t.Fatal("premise: the old process ended")
	default:
	}
	before, _ := os.ReadFile(g.snapshot())

	g.cmd = nil
	g.startEvery("200ms")
	newOnly := &affinityConversation{client: affinityClient1, sessionID: "provimux-test-new-only"}
	newKey := g.turn(newOnly, "new only: what is 3+3?")
	time.Sleep(600 * time.Millisecond) // periodic writes of the new process: the store is held
	if now, _ := os.ReadFile(g.snapshot()); !bytes.Equal(now, before) {
		t.Fatal("the new process wrote while the old one held the store")
	}
	// From here every version of the file is read: none may hold what only the given-up write held.
	seen := map[[32]byte]bool{}
	watch := func() {
		data, err := os.ReadFile(g.snapshot())
		if err != nil {
			return
		}
		sum := sha256.Sum256(data)
		if seen[sum] {
			return
		}
		seen[sum] = true
		plain := affinityDecrypt(t, g.key(), data)
		if bytes.Contains(plain, []byte("provimux-test-stalled-only")) {
			t.Fatal("the given-up write landed")
		}
	}
	watchFor := func(d time.Duration, until func() bool) {
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			watch()
			if until != nil && until() {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	_ = os.Remove(stall) // the old write resumes: it was given up, so it does not land
	watchFor(500*time.Millisecond, nil)
	_ = os.Remove(linger)
	ended := func() bool {
		select {
		case <-oldStopped:
			return true
		default:
			return false
		}
	}
	watchFor(10*time.Second, ended)
	if !ended() {
		t.Fatal("the old process did not end")
	}
	// The store is free: the new process writes on its next checks.
	watchFor(5*time.Second, func() bool {
		now, _ := os.ReadFile(g.snapshot())
		return !bytes.Equal(now, before) && bytes.Contains(affinityDecrypt(t, g.key(), now), []byte("provimux-test-new-only"))
	})
	if now, _ := os.ReadFile(g.snapshot()); !bytes.Contains(affinityDecrypt(t, g.key(), now), []byte("provimux-test-new-only")) {
		t.Fatalf("the new process never wrote:\n%s", g.log.String())
	}
	g.restart(false, false)
	probes := 0
	for _, c := range []struct {
		conversation *affinityConversation
		key          string
		back         bool
	}{{kept, keptKey, true}, {newOnly, newKey, true}, {stalledOnly, stalledKey, false}} {
		g.otherNext(c.key, &probes)
		got := g.turn(c.conversation, "after: what is 4+4?")
		if c.back && got != c.key {
			t.Errorf("%s: bound to %s, came back on %s", c.conversation.sessionID, c.key, got)
		}
		if !c.back && got == c.key {
			t.Errorf("%s: only the given-up write held it, yet it came back on %s", c.conversation.sessionID, got)
		}
	}
}

// affinityDecrypt opens a snapshot with the key beside it, as the format in
// sdk/cliproxy/provimux_affinity_persist.go describes: key file "PMXAFFKEY2\n" + 32 bytes + check;
// snapshot "PMXAFF2\n" + nonce + AES-256-GCM under HMAC-SHA256(key, "provimux affinity snapshot v2").
func affinityDecrypt(t *testing.T, keyPath string, data []byte) []byte {
	t.Helper()
	keyData, err := os.ReadFile(keyPath)
	if err != nil || len(keyData) < 11+32 {
		t.Fatalf("key: %v", err)
	}
	mac := hmac.New(sha256.New, keyData[11:43])
	mac.Write([]byte("provimux affinity snapshot v2"))
	block, _ := aes.NewCipher(mac.Sum(nil))
	aead, _ := cipher.NewGCM(block)
	const magic = "PMXAFF2\n"
	if len(data) < len(magic)+aead.NonceSize() {
		t.Fatal("short snapshot")
	}
	body := data[len(magic):]
	plain, err := aead.Open(nil, body[:aead.NonceSize()], body[aead.NonceSize():], []byte(magic))
	if err != nil {
		t.Fatalf("snapshot does not open: %v", err)
	}
	return plain
}
