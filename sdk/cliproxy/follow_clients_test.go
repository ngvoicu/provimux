package cliproxy

import (
	"bytes"
	"context"
	"fmt"
	cfgaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	access "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"io"
	"net/http"
	"strings"
	"testing"
)

func followCall(t *testing.T, h *sentHarness, method, path, body, key, ua string) (int, []byte, http.Header) {
	t.Helper()
	r, _ := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	if ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, e := h.server.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(resp.Body)
	if e != nil {
		t.Fatal(e)
	}
	return resp.StatusCode, raw, resp.Header
}
func followConfigureKeys(h *sentHarness, keys ...string) {
	cfg := h.service.currentConfig().SDKConfig
	cfg.APIKeys = keys
	cfgaccess.Register(&cfg)
	h.service.accessManager.SetProviders(access.RegisteredProviders())
}
func TestFollowClientsAndIncludes(t *testing.T) {
	includes := []string{"/v1/models", "/v1/models?include=accounts", "/v1/models?include=figures", "/v1/models?include=figures,accounts", "/v1/models?include=accounts&include=figures", "/v1/models?client_version=1&include=accounts", "/v1/models/review-public", "/v1beta/models", "/v1beta/models/review-public"}
	for _, restricted := range []bool{false, true} {
		t.Run(fmt.Sprint(restricted), func(t *testing.T) {
			h := newSentHarness(t, reviewRouter+fmt.Sprintf("models:\n  only-my-names:\n    enabled: %v\n", restricted))
			useAnsweredBook(t, newSentClock())
			type view struct {
				raw    []byte
				status int
			}
			vs := map[string]view{}
			for _, p := range includes {
				for _, ua := range []string{"fixture-client", "claude-cli/fixture"} {
					status, b, _ := followCall(t, h, "GET", p, "", sentClientKey, ua)
					vs[p+ua] = view{b, status}
				}
			}
			st, _, heads := followCall(t, h, "POST", "/v1/chat/completions", chatBody("review-public", false, ""), sentOtherClientKey, "")
			entry := heads.Get(logging.ProvimuxEntryHeader)
			if st != 200 || entry != "openrouter/lab/native" {
				t.Fatal("post fixture", st, entry)
			}
			for _, p := range includes {
				for _, ua := range []string{"fixture-client", "claude-cli/fixture"} {
					status, b, _ := followCall(t, h, "GET", p, "", sentClientKey, ua)
					old := vs[p+ua]
					if (status != old.status || !bytes.Equal(b, old.raw)) && p != "/v1/models/review-public" && p != "/v1beta/models/review-public" {
						t.Errorf("other key changed %s %s status=%d previous=%d", p, ua, status, old.status)
					}
					if bytes.Contains(b, []byte(entry)) {
						t.Errorf("other key exposed in %s %s", p, ua)
					}
				}
			}
			_, mine, _ := followCall(t, h, "GET", "/v1/models?include=accounts", "", sentOtherClientKey, "")
			if !bytes.Contains(mine, []byte(entry)) {
				t.Fatal("missing own history")
			}
			_, mg, _ := followCall(t, h, "GET", "/v8/management/provimux/model-routes", "", namesManagementKey, "")
			if !bytes.Contains(mg, []byte(entry)) {
				t.Fatal("management cannot see B")
			}
			followConfigureKeys(h, sentClientKey)
			status, _, _ := followCall(t, h, "GET", "/v1/models?include=accounts", "", sentOtherClientKey, "")
			if status != 401 {
				t.Fatalf("removed key got %d", status)
			}
			followConfigureKeys(h, sentClientKey, sentOtherClientKey, "fixture-fresh")
			_, again, _ := followCall(t, h, "GET", "/v1/models?include=accounts", "", sentOtherClientKey, "")
			_, fresh, _ := followCall(t, h, "GET", "/v1/models?include=accounts", "", "fixture-fresh", "")
			if !bytes.Contains(again, []byte(entry)) || bytes.Contains(fresh, []byte(entry)) {
				t.Error("key removal/re-add semantics")
			}
			followConfigureKeys(h)
			status, anon, _ := followCall(t, h, "GET", "/v1/models?include=accounts", "", "", "")
			if status != 200 || bytes.Contains(anon, []byte(entry)) {
				t.Fatalf("keyless view before anonymous answer %d", status)
			}
			status, _, _ = followCall(t, h, "POST", "/v1/chat/completions", chatBody("review-public", false, ""), "", "")
			_, anon, _ = followCall(t, h, "GET", "/v1/models?include=accounts", "", "", "second-keyless-caller")
			if status != 200 || !bytes.Contains(anon, []byte(entry)) {
				t.Error("two keyless callers did not share")
			}
			followConfigureKeys(h, sentClientKey, sentOtherClientKey)
			_, a, _ := followCall(t, h, "GET", "/v1/models?include=accounts", "", sentClientKey, "")
			if bytes.Contains(a, []byte(entry)) {
				t.Error("key A saw anonymous history")
			}
		})
	}
}

type followTeamAccess struct{}

func (followTeamAccess) Identifier() string { return "fixture-provider" }
func (followTeamAccess) Authenticate(_ context.Context, r *http.Request) (*access.Result, *access.AuthError) {
	for _, key := range []string{sentClientKey, sentOtherClientKey} {
		if r.Header.Get("Authorization") == "Bearer "+key {
			return &access.Result{Provider: "fixture-provider", Principal: "fixture-team"}, nil
		}
	}
	return nil, access.NewInvalidCredentialError()
}

// Finding 39, decided as intended (rule B.2 of the amendment of 2026-11-27 (2)): the record is per caller as the
// gateway authenticates it; an access provider of an embedding that maps two keys to one subject makes them one caller.
// Ported: the test pins that decided behaviour; the reviewer asked for two records.
func TestFollowTwoKeysSamePrincipal(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	useAnsweredBook(t, newSentClock())
	h.service.accessManager.SetProviders([]access.Provider{followTeamAccess{}})
	_, before, _ := followCall(t, h, "GET", "/v1/models?include=accounts", "", sentClientKey, "")
	s, _, headers := followCall(t, h, "POST", "/v1/chat/completions", chatBody("review-public", false, ""), sentOtherClientKey, "")
	entry := headers.Get(logging.ProvimuxEntryHeader)
	a, after, _ := followCall(t, h, "GET", "/v1/models?include=accounts", "", sentClientKey, "")
	if a != 200 || s != 200 || entry != "openrouter/lab/native" || bytes.Contains(before, []byte(entry)) {
		t.Fatal("fixture", a, s, entry)
	}
	t.Logf("distinct accepted keys, one access principal: key A sees B's entry=%v", bytes.Contains(after, []byte(entry)))
	if !bytes.Contains(after, []byte(entry)) {
		t.Error("two keys of one authenticated subject are no longer one caller")
	}
}
