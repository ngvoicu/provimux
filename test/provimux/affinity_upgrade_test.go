package provimux

// What an upgrade to this build does for a user who sets nothing about
// routing.session-affinity-persist (on by default whenever session affinity is on), on the
// process level, with the harness of affinity_restart_test.go: the gateway in a child process on
// loopback, a stand-in vendor with two API keys, stops with SIGTERM. The files are created by the
// write at the stop (snapshot interval one hour unless said otherwise), so nothing here waits for a
// periodic write.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

const affinityStatePath = "/v8/management/provimux/session-affinity-persist"

// stateOnce reads GET /v8/management/provimux/session-affinity-persist.
func (g *affinityGateway) stateOnce() (map[string]any, error) {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", g.port, affinityStatePath), nil)
	req.Header.Set("Authorization", "Bearer "+affinityManagement)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}
	var state map[string]any
	if errDecode := json.Unmarshal(body, &state); errDecode != nil {
		return nil, errDecode
	}
	return state, nil
}

func (g *affinityGateway) state() map[string]any {
	g.t.Helper()
	state, err := g.stateOnce()
	if err != nil {
		g.t.Fatalf("state: %v", err)
	}
	return state
}

// expectState checks the state's fields; a nil value means the field is absent.
func (g *affinityGateway) expectState(when string, want map[string]any) map[string]any {
	g.t.Helper()
	state := g.state()
	for field, value := range want {
		got, present := state[field]
		if value == nil {
			if present {
				g.t.Errorf("%s: %s = %v, want absent (state %v)", when, field, got, state)
			}
			continue
		}
		if !present || fmt.Sprint(got) != fmt.Sprint(value) {
			g.t.Errorf("%s: %s = %v, want %v (state %v)", when, field, got, value, state)
		}
	}
	return state
}

// affinityFiles lists every file of the auth folder whose name holds ".affinity": the two files,
// temporary files and probes.
func (g *affinityGateway) affinityFiles() []string {
	g.t.Helper()
	entries, err := os.ReadDir(filepath.Join(g.dir, "auths"))
	if err != nil {
		g.t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".affinity") {
			out = append(out, entry.Name())
		}
	}
	return out
}

// setPersist puts routing.session-affinity-persist into the config file, as a user would.
func (g *affinityGateway) setPersist(value bool) {
	g.t.Helper()
	data, err := os.ReadFile(g.config)
	if err != nil {
		g.t.Fatal(err)
	}
	const anchor = "  session-affinity: true\n"
	if !bytes.Contains(data, []byte(anchor)) || bytes.Contains(data, []byte("session-affinity-persist")) {
		g.t.Fatalf("premise: config\n%s", data)
	}
	data = bytes.Replace(data, []byte(anchor), []byte(fmt.Sprintf("%s  session-affinity-persist: %t\n", anchor, value)), 1)
	if errWrite := os.WriteFile(g.config, data, 0o600); errWrite != nil {
		g.t.Fatal(errWrite)
	}
}

// putSetting writes the switch through the console's configuration path.
func (g *affinityGateway) putSetting(value string) {
	g.t.Helper()
	req, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("http://127.0.0.1:%d/v8/management/config/routing/session-affinity-persist", g.port), strings.NewReader(value))
	req.Header.Set("Authorization", "Bearer "+affinityManagement)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK {
		g.t.Fatalf("PUT the switch: %d %s", resp.StatusCode, body)
	}
}

// waitLog waits until the running gateway has logged text (an event; the bound only a hung process
// reaches).
func (g *affinityGateway) waitLog(text string) {
	g.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		g.logMu.Lock()
		found := strings.Contains(g.log.String()[g.logFrom:], text)
		g.logMu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			g.t.Fatalf("the gateway never logged %q", text)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (g *affinityGateway) fail() {
	if g.t.Failed() {
		g.t.Log(g.log.String())
	}
}

// bindConversation starts a conversation with a session id and checks that its turns stay on one key.
func (g *affinityGateway) bindConversation(id string) (*affinityConversation, string) {
	g.t.Helper()
	c := &affinityConversation{client: affinityClient1, sessionID: id}
	key := g.turn(c, id+": what is 1+1?")
	if again := g.turn(c, id+": what is 2+2?"); again != key {
		g.t.Fatalf("%s: turn 2 on %s, bound to %s", id, again, key)
	}
	return c, key
}

func TestProvimuxAffinityUpgradeSetsNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stops the gateway with SIGTERM")
	}
	if os.Getenv(affinityGatewayEnv) != "" {
		t.Skip("inside the gateway process")
	}

	// Session affinity on, nothing else set: the first start of this build finds no file (as after
	// a build without the feature) and reports the feature in effect; its stop creates both files;
	// from then on each restart puts every conversation with an id back on its key.
	t.Run("session affinity on: the first start creates the files, restarts keep conversations", func(t *testing.T) {
		g := newAffinityGateway(t, affinityOptions{unset: true, clientKeys: true})
		defer g.fail()
		if files := g.affinityFiles(); len(files) != 0 {
			t.Fatalf("premise: %v", files)
		}
		g.startEvery("1h")
		state := g.expectState("first start", map[string]any{"setting": true, "set": false, "in_effect": true, "reason": nil,
			"start_note": "no snapshot", "restored_at_start": nil})
		for field, name := range map[string]string{"snapshot": "kv.db.affinity", "key": "kv.db.affinity-key"} {
			if path, _ := state[field].(string); filepath.Base(path) != name {
				t.Errorf("first start: %s = %q", field, path)
			}
		}
		type bound struct {
			c   *affinityConversation
			key string
		}
		var conversations []bound
		c, key := g.bindConversation("provimux-test-upgrade-0")
		conversations = append(conversations, bound{c, key})
		probes := 0
		for restart := 1; restart <= 2; restart++ {
			g.stop()
			for _, path := range []string{g.snapshot(), g.key()} {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("restart %d: %s not written (%v)", restart, filepath.Base(path), err)
				}
				if path == g.key() && info.Mode().Perm() != 0o600 {
					t.Errorf("restart %d: key mode %04o", restart, info.Mode().Perm())
				}
			}
			plain := affinityDecrypt(t, g.key(), mustRead(t, g.snapshot()))
			for _, b := range conversations {
				if !bytes.Contains(plain, []byte(b.c.sessionID)) {
					t.Fatalf("restart %d: %s not in the snapshot", restart, b.c.sessionID)
				}
			}
			g.startEvery("1h")
			state := g.expectState(fmt.Sprintf("restart %d", restart), map[string]any{"setting": true, "set": false, "in_effect": true, "reason": nil})
			restored, _ := state["restored_at_start"].(map[string]any)
			if sessions, _ := restored["sessions"].(float64); int(sessions) < len(conversations) {
				t.Errorf("restart %d: restored %v, want at least %d", restart, restored, len(conversations))
			}
			for _, b := range conversations {
				g.otherNext(b.key, &probes)
				if got := g.turn(b.c, fmt.Sprintf("after restart %d: what is 3+3?", restart)); got != b.key {
					t.Errorf("restart %d: %s bound to %s, came back on %s", restart, b.c.sessionID, b.key, got)
				}
			}
			c, key := g.bindConversation(fmt.Sprintf("provimux-test-upgrade-%d", restart))
			conversations = append(conversations, bound{c, key})
		}
		g.stop()
	})

	// Session affinity off, Home mode, or the KV store disabled, nothing else set: no file, at the
	// stop or at the periodic writes (every 200 ms), across two runs; the state names the cause (Home
	// mode serves no management API: TestProvimuxAffinityDefault checks its state).
	for _, tc := range []struct {
		name    string
		options affinityOptions
		reason  string
	}{
		{"session affinity off", affinityOptions{unset: true, clientKeys: true, affinityOff: true}, "session-affinity-off"},
		{"home mode", affinityOptions{unset: true, home: true}, "home-mode"},
		{"kv-store disabled", affinityOptions{unset: true, clientKeys: true, kvDisabled: true}, "kv-store-off"},
	} {
		t.Run(tc.name+": no file", func(t *testing.T) {
			g := newAffinityGateway(t, tc.options)
			defer g.fail()
			for run := 1; run <= 2; run++ {
				g.start()
				if tc.options.home {
					// Home mode has no management API: that it runs in Home mode shows in its log.
					g.waitLog("home config subscription")
				} else {
					g.expectState(fmt.Sprintf("run %d", run), map[string]any{"setting": true, "set": false, "in_effect": false, "reason": tc.reason,
						"restored_at_start": nil, "start_note": "nothing restored: " + tc.reason})
					// Requests with a session id (with session affinity off, no binding is made).
					c := &affinityConversation{client: affinityClient1, sessionID: fmt.Sprintf("provimux-test-off-%d", run)}
					g.turn(c, "what is 1+1?")
					g.turn(c, "what is 2+2?")
				}
				g.stop()
				if files := g.affinityFiles(); len(files) != 0 {
					t.Fatalf("run %d wrote %v", run, files)
				}
			}
		})
	}

	// A file of someone else's at one of the two names: nothing is written, replaced or removed, at
	// the stop or at the periodic writes, across two runs; the state says foreign-file and names it.
	for _, tc := range []struct {
		name    string
		at      func(*affinityGateway) string
		content string
	}{
		{"another program's file at the snapshot's name", (*affinityGateway).snapshot, "another program's data\n"},
		{"a file that only begins with our header at the snapshot's name", (*affinityGateway).snapshot, "PMXAFF2\n" + strings.Repeat("x", 64)},
		{"another program's file at the key's name", (*affinityGateway).key, "another program's key\n"},
		{"a file that only begins with our header at the key's name", (*affinityGateway).key, "PMXAFFKEY2\n" + strings.Repeat("y", 48)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newAffinityGateway(t, affinityOptions{unset: true, clientKeys: true})
			defer g.fail()
			foreign := tc.at(g)
			if err := os.WriteFile(foreign, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(foreign)
			for run := 1; run <= 2; run++ {
				g.start()
				state := g.expectState(fmt.Sprintf("run %d", run), map[string]any{"setting": true, "set": false, "in_effect": false,
					"reason": "foreign-file", "restored_at_start": nil})
				if detail, _ := state["detail"].(string); !strings.Contains(detail, filepath.Base(foreign)+" is not") {
					t.Errorf("run %d: detail %q does not name %s", run, detail, filepath.Base(foreign))
				}
				g.bindConversation(fmt.Sprintf("provimux-test-foreign-%d", run))
				g.stop()
				if now := mustRead(t, foreign); string(now) != tc.content {
					t.Fatalf("run %d: the foreign file was changed", run)
				}
				if after, _ := os.Stat(foreign); !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
					t.Fatalf("run %d: the foreign file was replaced", run)
				}
				if files := g.affinityFiles(); len(files) != 1 || files[0] != filepath.Base(foreign) {
					t.Fatalf("run %d: files %v", run, files)
				}
			}
		})
	}

	// Turning the switch off removes Provimux's own files: in the configuration file (they are gone
	// before the server serves), and in the console while running (within one check).
	t.Run("switch off in the configuration removes the files", func(t *testing.T) {
		g := newAffinityGateway(t, affinityOptions{unset: true, clientKeys: true})
		defer g.fail()
		g.startEvery("1h")
		g.bindConversation("provimux-test-switch-config")
		g.stop()
		if len(g.affinityFiles()) != 2 {
			t.Fatalf("premise: files %v", g.affinityFiles())
		}
		g.setPersist(false)
		g.startEvery("200ms")
		if files := g.affinityFiles(); len(files) != 0 {
			t.Fatalf("files after the start with the switch off: %v", files)
		}
		g.expectState("switched off", map[string]any{"setting": false, "set": true, "in_effect": false, "reason": "switch-off",
			"restored_at_start": nil, "last_error": nil})
		g.bindConversation("provimux-test-switch-config-off")
		g.stop()
		if files := g.affinityFiles(); len(files) != 0 {
			t.Fatalf("files after the stop with the switch off: %v", files)
		}
	})

	t.Run("switch off in the console removes the files", func(t *testing.T) {
		g := newAffinityGateway(t, affinityOptions{unset: true, clientKeys: true})
		defer g.fail()
		g.startEvery("1h")
		g.bindConversation("provimux-test-switch-console")
		g.stop()
		g.startEvery("1s")
		if len(g.affinityFiles()) != 2 {
			t.Fatalf("premise: files %v", g.affinityFiles())
		}
		// A change made before the gateway watches its config file is not applied until the next
		// change (upstream's start): the console is used once the watcher runs.
		g.waitLog("file watcher started")
		g.putSetting("false")
		// The reload of the file and the next check are events: wait for them, with a bound that only
		// a hung process reaches.
		deadline := time.Now().Add(60 * time.Second)
		for len(g.affinityFiles()) != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("files still there: %v", g.affinityFiles())
			}
			time.Sleep(50 * time.Millisecond)
		}
		g.expectState("switched off in the console", map[string]any{"setting": false, "set": true, "in_effect": false, "reason": "switch-off", "last_error": nil})
		g.bindConversation("provimux-test-switch-console-off")
		g.stop()
		if files := g.affinityFiles(); len(files) != 0 {
			t.Fatalf("files after the stop with the switch off: %v", files)
		}
		if data := mustRead(t, g.config); !bytes.Contains(data, []byte("session-affinity-persist: false")) {
			t.Fatalf("config file:\n%s", data)
		}
	})
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
