package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the account marker on every answer (answered-by.md 5 and 8.4). The first-byte matrix of
// server_auth_first_byte_test.go again, with models.answered-by on and two accounts of one kind and one host: the
// three X-CPA-AUTH-* headers are the same for both, so only X-Provimux-Account tells which answered. Each answer
// carries the marker of the account that answered, after a failover the second's; a keep-alive committed first, an
// error and a failure of every account carry none; the marker is present exactly when the three are. Keys and hosts
// are invented.

// provimuxAnsweredByTestKey is an invented key of the account markers.
var provimuxAnsweredByTestKey = []byte("provimux-api-test-marker-key-32b")

func provimuxAnsweredOn(t *testing.T) {
	t.Helper()
	auth.SetProvimuxMarkerKey(provimuxAnsweredByTestKey)
	was := internallogging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() {
		internallogging.SetProvimuxAnsweredBy(was)
		auth.SetProvimuxMarkerKey(nil)
	})
}

// provimuxCredential is a credential of the marker matrix: its three values, its marker, and what its upstream puts
// in the answer.
type provimuxCredential struct {
	auth      *auth.Auth
	statement internallogging.AuthStatement
	account   string
	text      string
}

// assertProvimuxCommit: the committed headers carry X-Provimux-Account exactly when they carry the three, and then
// the marker and the three are one credential's, committed by the write of that credential's answer (not a keep-alive
// or a flush), with the answer's status, on a body that carries its text and no other credential's. On the default
// configuration a body that carries a credential's text names it. It returns the named credential, or nil.
func assertProvimuxCommit(t *testing.T, r *firstCommitRecorder, credentials []provimuxCredential, answerStatus int, defaultConfig bool) *provimuxCredential {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	body := r.Body.String()
	got := internallogging.AuthStatement{
		Kind:     r.snapshot.Get(internallogging.CPAAuthKindHeader),
		Provider: r.snapshot.Get(internallogging.CPAAuthProviderHeader),
		Host:     r.snapshot.Get(internallogging.CPAAuthHostHeader),
	}
	account := r.snapshot.Get(internallogging.ProvimuxAccountHeader)
	for _, name := range internallogging.ProvimuxFieldNames()[1:] {
		if values := r.snapshot.Values(name.Header); len(values) != 0 {
			t.Fatalf("%s is written: %v", name.Header, values)
		}
	}
	if (got == internallogging.AuthStatement{}) != (account == "") {
		t.Fatalf("the three %+v and the marker %q are not together", got, account)
	}
	var named *provimuxCredential
	if account != "" {
		for i := range credentials {
			if credentials[i].statement == got && credentials[i].account == account {
				named = &credentials[i]
			}
		}
		if named == nil {
			t.Fatalf("headers name %+v %s: no credential's", got, account)
		}
		if !r.nextKnown || r.nextFlush || isKeepAliveWrite(r.nextBytes) {
			t.Fatalf("%s committed by a flush or a keep-alive %q, not by the write of an answer", account, r.nextBytes)
		}
		if r.status != answerStatus {
			t.Fatalf("%s committed with status %d, want %d", account, r.status, answerStatus)
		}
		for _, other := range credentials {
			has := strings.Contains(body, other.text)
			if other.auth.ID == named.auth.ID && !has || other.auth.ID != named.auth.ID && has {
				t.Fatalf("headers name %s (%s); body %q", named.auth.ID, account, body)
			}
		}
	}
	if defaultConfig {
		for _, credential := range credentials {
			if strings.Contains(body, credential.text) && (named == nil || named.auth.ID != credential.auth.ID) {
				t.Fatalf("default configuration: body from %s, headers name %v", credential.auth.ID, named)
			}
		}
	}
	return named
}

func checkProvimuxCell(t *testing.T, recorder *firstCommitRecorder, credentials []provimuxCredential, scenario firstByteScenario, configuration firstByteConfiguration) {
	t.Helper()
	if credentials[0].statement != credentials[1].statement || credentials[0].account == credentials[1].account || credentials[0].account == "" {
		t.Fatalf("fixture: want one statement and two markers, got %+v %q and %+v %q", credentials[0].statement, credentials[0].account, credentials[1].statement, credentials[1].account)
	}
	named := assertProvimuxCommit(t, recorder, credentials, http.StatusOK, !configuration.keepAlive)
	body := recorder.Body.String()
	if scenario.answering < 0 {
		if named != nil {
			t.Fatalf("all failed, headers name %s", named.auth.ID)
		}
		return
	}
	want := credentials[scenario.answering]
	if !strings.Contains(body, want.text) {
		t.Fatalf("fixture: body %q (status %d) lacks %s's text %q", body, recorder.Code, want.auth.ID, want.text)
	}
	if !configuration.keepAlive && (named == nil || named.auth.ID != want.auth.ID) {
		t.Fatalf("default configuration: %s answered, headers name %v", want.auth.ID, named)
	}
}

func runProvimuxProtocolCell(t *testing.T, path firstBytePath, scenario firstByteScenario, configuration firstByteConfiguration) {
	t.Helper()
	provimuxAnsweredOn(t)
	cfg := firstByteConfig(configuration)
	server := newTestServerWithConfig(t, cfg)
	manager := server.handlers.AuthManager
	manager.SetRetryConfig(0, 0, 3)
	recorder := newFirstCommitRecorder()

	statement := internallogging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "shared-gateway.example.test"}
	key := func(id, apiKey, priority string) *auth.Auth {
		a := &auth.Auth{ID: id, Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": apiKey, "base_url": "https://shared-gateway.example.test", "priority": priority}}
		if path.count { // a key with a base URL of its own is counted locally by the Claude executor
			delete(a.Attributes, "base_url")
		}
		return a
	}
	if path.count {
		statement.Host = ""
	}
	texts := [2]string{"served-by-provimux-first-key", "served-by-provimux-second-key"}
	if path.count {
		texts = [2]string{"4101", "4202"}
	}
	usage := [2]int{4, 4}
	if path.compact {
		usage = [2]int{5101, 5202}
	}
	credentials := []provimuxCredential{
		{auth: key("provimux-first-key", "fixture-key-first", "10"), statement: statement, text: texts[0]},
		{auth: key("provimux-second-key", "fixture-key-second", "0"), statement: statement, text: texts[1]},
	}
	if path.compact {
		credentials[0].text, credentials[1].text = `"input_tokens":5101`, `"input_tokens":5202`
	}
	transport := &firstByteClaudeTransport{byToken: map[string]int{"fixture-key-first": 0, "fixture-key-second": 1}, behaviour: scenario.behaviour, markers: texts, usage: usage}
	if configuration.keepAlive && path.keepAlive {
		transport.flushed = recorder.flushed
	}
	manager.SetRoundTripperProvider(transport)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
	for i := range credentials {
		credentials[i].account = credentials[i].auth.ProvimuxAccountMarker()
		if _, err := manager.Register(context.Background(), credentials[i].auth); err != nil {
			t.Fatalf("register: %v", err)
		}
		registry.GetGlobalRegistry().RegisterClient(credentials[i].auth.ID, "claude", []*registry.ModelInfo{{ID: wireModelForMatrix, OwnedBy: "anthropic", Created: 1700000000}})
		id := credentials[i].auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	req := httptest.NewRequest(path.method, path.path, strings.NewReader(path.body))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)
	checkProvimuxCell(t, recorder, credentials, scenario, configuration)
}

func runProvimuxMediaCell(t *testing.T, path firstByteMediaPath, scenario firstByteScenario, configuration firstByteConfiguration) {
	t.Helper()
	provimuxAnsweredOn(t)
	cfg := firstByteConfig(configuration)
	server := newTestServerWithConfig(t, cfg)
	manager := server.handlers.AuthManager
	manager.SetRetryConfig(0, 0, 3)
	recorder := newFirstCommitRecorder()
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = io.WriteString(w, "video-bytes-"+strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".mp4"))
	}))
	t.Cleanup(download.Close)

	host := "shared-" + path.provider + ".example.test"
	credentials := []provimuxCredential{}
	behaviour := map[string]firstByteBehaviour{}
	for i, label := range []string{"first", "second"} {
		// The ids of server_auth_first_byte_test.go: a video's later fetch is bound to the credential that made it, by
		// id, in a store the process keeps; the two matrices then agree whichever runs first.
		id := "matrix-" + label + "-" + path.provider
		a := &auth.Auth{ID: id, Provider: path.provider, Status: auth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "fixture-" + label, "base_url": "https://" + host + "/v1", "priority": fmt.Sprint(10 - 10*i)}}
		credentials = append(credentials, provimuxCredential{auth: a, statement: internallogging.AuthStatement{Kind: "apikey", Provider: path.provider, Host: host}, account: a.ProvimuxAccountMarker(), text: "served-by-" + id})
		behaviour[id] = scenario.behaviour[i]
	}
	executor := &firstByteMediaExecutor{provider: path.provider, behaviour: behaviour, contentURL: download.URL}
	if configuration.keepAlive && path.keepAlive {
		executor.flushed = recorder.flushed
	}
	manager.RegisterExecutor(executor)
	for _, credential := range credentials {
		if _, err := manager.Register(context.Background(), credential.auth); err != nil {
			t.Fatalf("register: %v", err)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.auth.ID, credential.auth.Provider, []*registry.ModelInfo{{ID: path.model, Type: path.modelType, OwnedBy: path.provider, Created: 1700000000}})
		manager.RefreshSchedulerEntry(credential.auth.ID)
		id := credential.auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	var body io.Reader
	if path.body != "" {
		body = strings.NewReader(path.body)
	}
	req := httptest.NewRequest(path.method, path.path, body)
	req.Header.Set("Authorization", "Bearer test-key")
	if path.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	server.engine.ServeHTTP(recorder, req)
	if path.download && scenario.answering >= 0 {
		for i := range credentials {
			credentials[i].text = "video-bytes-" + credentials[i].text
		}
	}
	checkProvimuxCell(t, recorder, credentials, scenario, configuration)
}

func TestProvimuxAnswerNamesTheAccountAtItsFirstByte(t *testing.T) {
	cells := 0
	t.Run("protocols", func(t *testing.T) {
		for _, path := range firstByteProtocolPaths() {
			for _, scenario := range firstByteScenarios {
				if scenario.streams && !path.stream {
					continue
				}
				for _, configuration := range firstByteConfigurations {
					cells++
					path, scenario, configuration := path, scenario, configuration
					t.Run(fmt.Sprintf("row %s %s %s stream=%t/%s/%s", path.row, path.method, path.path, path.stream, scenario.name, configuration.name), func(t *testing.T) {
						runProvimuxProtocolCell(t, path, scenario, configuration)
					})
				}
			}
		}
	})
	t.Run("media", func(t *testing.T) {
		for _, path := range firstByteMediaPaths() {
			for _, scenario := range firstByteScenarios {
				if scenario.streams && !path.stream || path.bound && scenario.answering == 1 {
					continue
				}
				for _, configuration := range firstByteConfigurations {
					cells++
					path, scenario, configuration := path, scenario, configuration
					t.Run(fmt.Sprintf("row %s %s %s stream=%t/%s/%s", path.row, path.method, path.path, path.stream, scenario.name, configuration.name), func(t *testing.T) {
						runProvimuxMediaCell(t, path, scenario, configuration)
					})
				}
			}
		}
	})
	// Row 32, a relay: it names the credential it holds itself, the marker with it.
	t.Run("row 32 alpha search", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			httpErr error
			status  int
		}{
			{name: "upstream answered", status: http.StatusOK},
			{name: "upstream answered an error", status: http.StatusTooManyRequests},
			{name: "upstream unreachable", httpErr: errors.New("fixture upstream unreachable"), status: http.StatusBadGateway},
		} {
			cells++
			t.Run(test.name, func(t *testing.T) {
				provimuxAnsweredOn(t)
				server := newTestServer(t)
				executor := &codexSearchCaptureExecutor{httpErr: test.httpErr, responseBody: io.NopCloser(strings.NewReader(`{"marker":"served-by-codex-login"}`))}
				if test.httpErr == nil {
					executor.statuses = []int{test.status}
				}
				server.handlers.AuthManager.RegisterExecutor(executor)
				credential := &auth.Auth{ID: "codex-login", Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "made-up-token"}}
				if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
					t.Fatalf("register: %v", err)
				}
				req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"q"}`))
				req.Header.Set("Authorization", "Bearer test-key")
				req.Header.Set("Content-Type", "application/json")
				recorder := newFirstCommitRecorder()
				server.engine.ServeHTTP(recorder, req)
				if recorder.Code != test.status {
					t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
				}
				credentials := []provimuxCredential{{auth: credential, statement: internallogging.AuthStatement{Kind: "oauth", Provider: "codex"}, account: credential.ProvimuxAccountMarker(), text: "served-by-codex-login"}}
				named := assertProvimuxCommit(t, recorder, credentials, test.status, true)
				if (named != nil) != (test.httpErr == nil) || credentials[0].account == "" {
					t.Fatalf("named %v for %s (marker %q)", named != nil, test.name, credentials[0].account)
				}
			})
		}
	})
	t.Logf("matrix: %d cells with the marker", cells)
}
