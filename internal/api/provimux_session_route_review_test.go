package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the review's cases of session-route G1 through the real router (findings 3 and 10). They use only the
// helpers of provimux_session_route_test.go as it was before the review, so that the same file runs on the reviewed
// commit, where they fail, and here, where they pass. Every key, account and session is invented.

// srBound reports whether the caller's conversation is still bound.
func (f *srAPIFixture) srBound(key string, header http.Header) bool {
	f.t.Helper()
	_, body := f.show(key, header, `{"model":"`+srAPIModel+`"}`)
	return bytes.Contains(body, []byte(`"conversation":"bound"`))
}

// TestProvimuxSessionRouteReleaseRequests is finding 3 (b) for the release: an expect that is present must be a
// well-formed token, and a body that does not parse is refused; each is 400 and releases nothing. Leaving expect out
// releases, as documented.
func TestProvimuxSessionRouteReleaseRequests(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"empty expect", `{"model":"` + srAPIModel + `","expect":""}`, http.StatusBadRequest},
		{"blank expect", `{"model":"` + srAPIModel + `","expect":" "}`, http.StatusBadRequest},
		{"null expect", `{"model":"` + srAPIModel + `","expect":null}`, http.StatusBadRequest},
		{"malformed expect", `{"model":"` + srAPIModel + `","expect":"b.x"}`, http.StatusBadRequest},
		{"number expect", `{"model":"` + srAPIModel + `","expect":7}`, http.StatusBadRequest},
		{"trailing bracket", `{"model":"` + srAPIModel + `"}] `, http.StatusBadRequest},
		{"second object", `{"model":"` + srAPIModel + `"}{}`, http.StatusBadRequest},
		{"model twice", `{"model":"` + srAPIModel + `","model":"` + srAPIModel + `"}`, http.StatusBadRequest},
		{"null model", `{"model":null}`, http.StatusBadRequest},
		{"no expect", `{"model":"` + srAPIModel + `"}`, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSRAPIFixture(t)
			header := http.Header{"X-Session-Affinity": {"self"}}
			f.turn("sk-sr-one", header)
			code, body := f.release("sk-sr-one", header, c.body)
			if code != c.want {
				t.Fatalf("%s: HTTP %d, want %d: %s", c.body, code, c.want, body)
			}
			if released := bytes.Contains(body, []byte(`"released":true`)); released != (c.want == http.StatusOK) {
				t.Fatalf("%s: released %v: %s", c.body, released, body)
			}
			if bound := f.srBound("sk-sr-one", header); bound != (c.want != http.StatusOK) {
				t.Fatalf("%s: still bound %v", c.body, bound)
			}
		})
	}
}

// TestProvimuxSessionRouteShowRequests is finding 3 (b) for the show: the same reading of every field.
func TestProvimuxSessionRouteShowRequests(t *testing.T) {
	f := newSRAPIFixture(t)
	for _, c := range []struct {
		body string
		want int
	}{
		{``, http.StatusBadRequest},
		{`[]`, http.StatusBadRequest},
		{`"model"`, http.StatusBadRequest},
		{`{}`, http.StatusBadRequest},
		{`{"model":""}`, http.StatusBadRequest},
		{`{"model":" "}`, http.StatusBadRequest},
		{`{"model":null}`, http.StatusBadRequest},
		{`{"model":7}`, http.StatusBadRequest},
		{`{"model":"m","model":"m"}`, http.StatusBadRequest},
		{`{"model":"m","identity":null}`, http.StatusBadRequest},
		{`{"model":"m","identity":[]}`, http.StatusBadRequest},
		{`{"model":"m","identity":"pck"}`, http.StatusBadRequest},
		{`{"model":"m","expect":"b.aaaaaaaaaaaaaaaa"}`, http.StatusBadRequest},
		{`{"model":"m","extra":1}`, http.StatusBadRequest},
		{`{"model":"m"}]`, http.StatusBadRequest},
		{`{"model":"m"} {"model":"m"}`, http.StatusBadRequest},
		{`{"model":"m"}`, http.StatusOK},
		{` {"model":"m","identity":{"prompt_cache_key":"p"}} `, http.StatusOK},
	} {
		if code, body := f.show("sk-sr-one", nil, c.body); code != c.want {
			t.Fatalf("%q: HTTP %d, want %d: %s", c.body, code, c.want, body)
		}
	}
}

// TestProvimuxSessionRouteAccountNoLongerListed is finding 10 (a): after the bound account is disabled and given
// another host, GET /v1/models?include=accounts no longer shows it, and neither does the show: it says only that the
// conversation is bound to an account no longer listed.
func TestProvimuxSessionRouteAccountNoLongerListed(t *testing.T) {
	f := newSRAPIFixture(t)
	header := http.Header{"X-Session-Affinity": {"own-row"}}
	f.turn("sk-sr-one", header)
	for _, a := range f.manager.List() {
		if a.Priority() != 10 {
			continue
		}
		a.Disabled = true
		a.Attributes["base_url"] = "https://changed.fixture.example.test"
		a.Attributes["api_key"] = "synthetic"
		if _, err := f.manager.Update(auth.WithSkipPersist(t.Context()), a); err != nil {
			t.Fatal(err)
		}
	}
	_, list := f.do(http.MethodGet, "/v1/models?include=accounts", "sk-sr-one", nil, "")
	_, show := f.show("sk-sr-one", header, `{"model":"`+srAPIModel+`"}`)
	if bytes.Contains(list, []byte("changed.fixture.example.test")) {
		t.Fatalf("the list shows the disabled account: %s", list)
	}
	if bytes.Contains(show, []byte("changed.fixture.example.test")) {
		t.Fatalf("the show gives what the list does not: %s", show)
	}
	var v struct {
		Conversation string `json:"conversation"`
		Bound        struct {
			Route json.RawMessage `json:"route"`
		} `json:"bound"`
	}
	mustUnmarshal(t, show, &v)
	if v.Conversation != "bound" || string(v.Bound.Route) != `{"listed":false}` {
		t.Fatalf("an account no longer listed: %s", show)
	}
}

// TestProvimuxSessionRouteAuto is finding 10 (b): a turn sent as model "auto" is found when asked for as "auto",
// resolved as the request path resolves it, and the answer names the model it was routed to (a model of
// GET /v1/models).
func TestProvimuxSessionRouteAuto(t *testing.T) {
	f := newSRAPIFixture(t)
	header := http.Header{"X-Session-Affinity": {"auto-conversation"}}
	if code, body := f.do(http.MethodPost, "/v1/chat/completions", "sk-sr-one", header, `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`); code != http.StatusOK {
		t.Fatalf("turn: %d %s", code, body)
	}
	_, show := f.show("sk-sr-one", header, `{"model":"auto"}`)
	var v struct {
		Conversation string  `json:"conversation"`
		Model        string  `json:"model"`
		RoutedModel  *string `json:"routed_model"`
	}
	mustUnmarshal(t, show, &v)
	if v.Conversation != "bound" || v.Model != "auto" || v.RoutedModel == nil || strings.TrimSpace(*v.RoutedModel) == "" || *v.RoutedModel == "auto" {
		t.Fatalf("auto: %s", show)
	}
	_, list := f.do(http.MethodGet, "/v1/models", "sk-sr-one", nil, "")
	if !bytes.Contains(list, []byte(`"id":"`+*v.RoutedModel+`"`)) {
		t.Fatalf("routed to %q, which GET /v1/models does not list", *v.RoutedModel)
	}
}
