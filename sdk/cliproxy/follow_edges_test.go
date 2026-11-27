package cliproxy

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Finding 40, the reviewer's synchronised reproduction, unchanged: the newest note is first (rule B.3).
func TestFollowClockInterleave(t *testing.T) {
	first, second, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var n atomic.Int32
	b := newProvimuxAnsweredBook(func() time.Time {
		i := n.Add(1)
		if i == 1 {
			close(first)
			<-release
		}
		if i == 2 {
			close(second)
		}
		return time.Unix(int64(i), 0)
	})
	d1, d2 := make(chan struct{}), make(chan struct{})
	go func() { b.note("k", "c", "m", "n", "p/first"); close(d1) }()
	<-first
	go func() { b.note("k", "c", "m", "n", "p/second"); close(d2) }()
	select {
	case <-second:
		<-d2
	case <-time.After(5 * time.Millisecond):
	}
	close(release)
	<-d1
	<-d2
	got := b.read("k", false, "c", "m", "n")
	if len(got) != 2 || got[0].Entry != "p/second" {
		t.Errorf("time-ordered result %v", got)
	}
}
func TestFollowWebsocketClients(t *testing.T) {
	h := newSentHarness(t, sentMatrixConfig)
	useAnsweredBook(t, newSentClock())
	auths := sentLogins(t, h)
	model := "provimux-test-codex-login"
	conn, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.server.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + sentOtherClientKey}})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if e = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"provimux-test-codex-login","input":[{"type":"message","role":"user","content":"fixture"}]}`)); e != nil {
		t.Fatal(e)
	}
	conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	var entry string
	for {
		_, raw, e := conn.ReadMessage()
		if e != nil {
			t.Fatal(e)
		}
		switch ojson.GetBytes(raw, "type").String() {
		case "response.created":
			entry = ojson.GetBytes(raw, logging.ProvimuxEntryField).String()
		case "response.completed":
			goto complete
		case "error":
			t.Fatalf("fixture %s", raw)
		}
	}
complete:
	if entry != "openai-codex/"+model {
		t.Fatalf("fixture received entry %q", entry)
	}
	// The record is written after the frame is: wait for the note, never for a fixed time.
	waitAnswered(t, func() bool {
		return len(ca.ProvimuxAnswered(ca.ProvimuxClientRef(sentOtherClientKey), auths["codex"].ID, auths["codex"].ProvimuxAccountMarker(), model)) > 0
	})
	a, m, _ := h.listsRow(sentClientKey, model, auths["codex"].ProvimuxAccountMarker())
	b, _, _ := h.listsRow(sentOtherClientKey, model, auths["codex"].ProvimuxAccountMarker())
	if a == nil || b == nil || m == nil {
		t.Fatal("missing rows")
	}
	t.Logf("websocket B received %q; A=%v B=%v management=%v", entry, a.items(), b.items(), m.items())
	if len(a.items()) != 0 || len(b.items()) != 1 || len(m.items()) != 1 {
		t.Error("WebSocket turn did not reach only its own key and management")
	}
}
