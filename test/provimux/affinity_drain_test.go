package provimux

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
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
