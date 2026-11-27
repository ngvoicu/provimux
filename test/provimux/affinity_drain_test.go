package provimux

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

// A stop with an answer in flight and the restart drain on (server.restart-drain-seconds): the stop writes the
// bindings first, the conversation whose answer is in flight included (it was bound when its answer started), then
// waits for that answer, which the client receives whole; the next start puts both conversations back on their keys.
// Nothing waits on a clock: the vendor holds the answer until the test has seen the snapshot written.
func TestProvimuxAffinityStopWithAnswersInFlight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stops the gateway with SIGTERM")
	}
	if os.Getenv(affinityGatewayEnv) != "" {
		t.Skip("inside the gateway process")
	}
	g := newAffinityGateway(t, affinityOptions{unset: true, clientKeys: true, drain: 20})
	defer g.fail()
	held, release := make(chan string, 1), make(chan struct{})
	g.vendor.mu.Lock()
	g.vendor.held, g.vendor.release = held, release
	g.vendor.mu.Unlock()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	g.startEvery("1h") // no periodic write: the snapshot appears at the stop only
	done, doneKey := g.bindConversation("provimux-test-drain-done")

	// An answer in flight: the vendor holds it.
	inFlight := &affinityConversation{client: affinityClient1, sessionID: "provimux-test-drain-in-flight"}
	type answer struct {
		status int
		body   string
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		payload, _ := json.Marshal(map[string]any{"model": affinityModel, "prompt_cache_key": inFlight.sessionID,
			"messages": []map[string]string{{"role": "user", "content": "[hold] a long answer, please"}}})
		req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", g.port), bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+inFlight.client)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Client-Request-Id", inFlight.sessionID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			answered <- answer{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		answered <- answer{status: resp.StatusCode, body: string(body)}
	}()
	inFlightKey := <-held
	if _, err := os.Stat(g.snapshot()); err == nil {
		t.Fatal("premise: a snapshot before the stop")
	}

	_ = g.cmd.Process.Signal(syscall.SIGTERM)
	// The write at stop comes first: wait for the file (an event; the bound only a hung process reaches).
	deadline := time.Now().Add(60 * time.Second)
	for {
		if data, err := os.ReadFile(g.snapshot()); err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no snapshot written at the stop:\n%s", g.log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-g.stopped:
		t.Fatal("the gateway ended with an answer in flight and a drain of 20 seconds")
	default:
	}
	plain := affinityDecrypt(t, g.key(), mustRead(t, g.snapshot()))
	for _, id := range []string{done.sessionID, inFlight.sessionID} {
		if !bytes.Contains(plain, []byte(id)) {
			t.Fatalf("%s is not in the snapshot written at the stop", id)
		}
	}
	close(release)
	released = true
	got := <-answered
	if got.err != nil || got.status != http.StatusOK || !strings.Contains(got.body, "answer from "+inFlightKey) {
		t.Fatalf("the answer in flight: %d %v %s", got.status, got.err, got.body)
	}
	<-g.stopped
	g.cmd = nil
	g.logMu.Lock()
	stopLog := g.log.String()[g.logFrom:]
	g.logMu.Unlock()
	if !strings.Contains(stopLog, "stopping: waiting up to 20 seconds for the answers in flight") {
		t.Fatalf("the stop did not drain:\n%s", stopLog)
	}

	g.startEvery("1h")
	state := g.expectState("after the drained stop", map[string]any{"in_effect": true})
	restored, _ := state["restored_at_start"].(map[string]any)
	if sessions, _ := restored["sessions"].(float64); int(sessions) < 2 {
		t.Errorf("restored %v, want both conversations", restored)
	}
	probes := 0
	for _, c := range []struct {
		conversation *affinityConversation
		key          string
	}{{done, doneKey}, {inFlight, inFlightKey}} {
		g.otherNext(c.key, &probes)
		if key := g.turn(c.conversation, "after the restart: what is 5+5?"); key != c.key {
			t.Errorf("%s: bound to %s, came back on %s", c.conversation.sessionID, c.key, key)
		}
	}
	g.stop()
}

// drainState reads GET /v8/management/provimux/restart-drain.
func (g *affinityGateway) drainState() map[string]any {
	g.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v8/management/provimux/restart-drain", g.port), nil)
	req.Header.Set("Authorization", "Bearer "+affinityManagement)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var state map[string]any
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &state) != nil {
		g.t.Fatalf("restart-drain state: %d %s", resp.StatusCode, body)
	}
	return state
}

// countLog counts text in the running process's log.
func (g *affinityGateway) countLog(text string) int {
	g.logMu.Lock()
	defer g.logMu.Unlock()
	return strings.Count(g.log.String()[g.logFrom:], text)
}

// The restart drain's state follows the running process: a hot reload of a value the gateway refuses ("12" in quotes,
// 30.0) leaves the reported wait as it was, and the state says what the file writes and that it is refused; a valid
// value reloaded is reported at once; the stop then waits what the state reported; a cold start with the refused value
// fails at loading the configuration. Each step waits for an event (the reload's log line, the state), not a clock.
func TestProvimuxRestartDrainStateFollowsTheRunningProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stops the gateway with SIGTERM")
	}
	if os.Getenv(affinityGatewayEnv) != "" {
		t.Skip("inside the gateway process")
	}
	g := newAffinityGateway(t, affinityOptions{unset: true, clientKeys: true, drain: 10})
	defer g.fail()
	g.startEvery("1h")
	g.waitLog("file watcher started")
	expect := func(when string, want map[string]any) {
		t.Helper()
		got := g.drainState()
		for field, value := range want {
			if fmt.Sprint(got[field]) != fmt.Sprint(value) {
				t.Fatalf("%s: %s = %v, want %v (state %v)", when, field, got[field], value, got)
			}
		}
	}
	expect("started with 10", map[string]any{"seconds": 10, "set": true, "written": "10", "written_accepted": true})

	write := func(value string) {
		t.Helper()
		data := mustRead(t, g.config)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "  restart-drain-seconds:") {
				lines[i] = "  restart-drain-seconds: " + value
			}
		}
		if err := os.WriteFile(g.config, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for limit := time.Now().Add(60 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(limit) {
				t.Fatalf("timed out waiting for %s:\n%s", what, g.log.String())
			}
		}
	}
	refused := 0
	for _, value := range []string{`"12"`, "30.0"} {
		write(value)
		refused++
		n := refused
		waitFor("the refused reload of "+value, func() bool { return g.countLog("failed to reload config") >= n })
		expect("the file at "+value+", 10 running", map[string]any{"seconds": 10, "waits_at_most": 10, "set": true, "written": value, "written_accepted": false})
		if got := g.drainState(); !strings.Contains(fmt.Sprint(got["written_error"]), "whole number") || got["written_seconds"] != nil {
			t.Fatalf("refused %s: %v", value, got)
		}
	}
	write("7")
	waitFor("the reload of 7", func() bool { return fmt.Sprint(g.drainState()["seconds"]) == "7" })
	expect("reloaded 7", map[string]any{"seconds": 7, "written": "7", "written_accepted": true, "written_seconds": 7})
	write(`"12"`)
	refused++
	waitFor(`the refused reload of "12" over 7`, func() bool { return g.countLog("failed to reload config") >= refused })
	expect(`the file at "12", 7 running`, map[string]any{"seconds": 7, "written_accepted": false})
	g.stop()
	if !strings.Contains(g.log.String()[g.logFrom:], "stopping: waiting up to 7 seconds for the answers in flight") {
		t.Fatalf("the stop did not use the reported 7 seconds:\n%s", g.log.String()[g.logFrom:])
	}

	// A cold start with the refused value fails at loading the configuration.
	cmd := exec.Command(os.Args[0], "-test.run=^TestProvimuxAffinityGatewayProcess$", "-test.v")
	cmd.Env = append(os.Environ(), affinityGatewayEnv+"="+g.config, "HOME="+g.dir, "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "NO_PROXY=")
	cmd.Dir = g.dir
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "restart-drain-seconds must be a whole number") {
		t.Fatalf("a cold start with \"12\": err=%v\n%s", err, out)
	}
}
