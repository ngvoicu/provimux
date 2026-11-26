package auth

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/regexp"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// Provimux: the account marker, answered-by.md 2.2 and 2.3, row by row. Every login, key, e-mail and id here is
// invented; none is taken from a real account.

var provimuxMarkerPattern = regexp.MustCompile(`^[as]\.[a-z2-7]{20}$`)

// provimuxTestMarkerKey is an invented key of 32 bytes.
var provimuxTestMarkerKey = []byte("provimux-test-marker-key-32bytes")

func withProvimuxMarkerKey(t *testing.T, key []byte) {
	t.Helper()
	previous := provimuxMarkerKey.Load()
	SetProvimuxMarkerKey(key)
	wasOn := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() {
		provimuxMarkerKey.Store(previous)
		logging.SetProvimuxAnsweredBy(wasOn)
	})
}

func markerOf(t *testing.T, a *Auth) string {
	t.Helper()
	marker := a.ProvimuxAccountMarker()
	if !provimuxMarkerPattern.MatchString(marker) {
		t.Fatalf("marker of %s = %q, want %s", a.ID, marker, provimuxMarkerPattern)
	}
	return marker
}

// provimuxFixtureIDToken is an unsigned id token with the given OpenAI auth claims, as a Codex login file holds one.
func provimuxFixtureIDToken(sub, workspace, user string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims := `{"sub":"` + sub + `","https://api.openai.com/auth":{"chatgpt_account_id":"` + workspace + `","chatgpt_user_id":"` + user + `"}}`
	return header + "." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".fixture-signature"
}

// loginFile is a credential loaded from a login file: the id is the file's name.
func loginFile(id, provider string, metadata map[string]any) *Auth {
	return &Auth{ID: id, FileName: id, Provider: provider, Attributes: map[string]string{AttributePath: "/fixture/auths/" + id, AttributeSource: "/fixture/auths/" + id}, Metadata: metadata}
}

// configKey is an API key of the configuration, at a position in its list.
func configKey(id, provider, key, baseURL string, position int) *Auth {
	attrs := map[string]string{AttributeSource: "config:" + provider + "[" + id + "]", AttributeConfigIndex: string(rune('0' + position))}
	if key != "" {
		attrs[AttributeAPIKey] = key
	}
	if baseURL != "" {
		attrs["base_url"] = baseURL
	}
	return &Auth{ID: id, Provider: provider, Attributes: attrs}
}

// provimuxMarkerRow is one row of the table of 2.3: two credentials of one account, the same file holding another
// account's login, and whether the marker follows the login ("changes") or the file.
type provimuxMarkerRow struct {
	name   string
	basis  string
	first  *Auth // an account's credential
	twin   *Auth // another credential of the same account (another file, another config row)
	taken  *Auth // first's file (or row) holding another account's login
	sameAs bool  // twin has first's marker
	follow bool  // taken has another marker than first
}

func provimuxMarkerRows() []provimuxMarkerRow {
	claudeOrg, claudeAccount := "0f0e0d0c-0b0a-4909-8807-060504030201", "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	madeUp := uuid.NewSHA1(uuid.MustParse("6ba7b812-9dad-11d1-80b4-00c04fd430c8"), []byte("fixture-made-up")).String()
	return []provimuxMarkerRow{
		{
			name: "Claude login", basis: "a", sameAs: true, follow: true,
			first: loginFile("claude-ada.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-a", "organization_uuid": claudeOrg, "account_uuid": claudeAccount}),
			twin:  loginFile("claude-ada-copy.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-b", "organization_uuid": claudeOrg, "account_uuid": claudeAccount}),
			taken: loginFile("claude-ada.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-c", "organization_uuid": claudeOrg, "account_uuid": "2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e"}),
		},
		{
			name: "Claude login without an organisation id", basis: "s", sameAs: false, follow: false,
			first: loginFile("claude-bo.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-a", "account_uuid": claudeAccount}),
			twin:  loginFile("claude-bo-copy.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-b", "account_uuid": claudeAccount}),
			taken: loginFile("claude-bo.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-c", "account_uuid": "2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e"}),
		},
		{
			name: "Claude login whose account id the gateway made up", basis: "s", sameAs: false, follow: false,
			first: loginFile("claude-cy.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-a", "organization_uuid": claudeOrg, "account_uuid": madeUp}),
			twin:  loginFile("claude-cy-copy.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-b", "organization_uuid": claudeOrg, "account_uuid": madeUp}),
			taken: loginFile("claude-cy.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-c", "organization_uuid": "9f9e9d9c-9b9a-4999-8998-979695949392", "account_uuid": madeUp}),
		},
		{
			name: "Codex login", basis: "a", sameAs: true, follow: true,
			first: loginFile("codex-ada.json", "codex", map[string]any{"type": "codex", "access_token": "fixture-a", "account_id": "ws-fixture-1", "id_token": provimuxFixtureIDToken("sub-ada", "ws-fixture-1", "user-ada")}),
			twin:  loginFile("codex-ada-copy.json", "codex", map[string]any{"type": "codex", "access_token": "fixture-b", "account_id": "ws-fixture-1", "id_token": provimuxFixtureIDToken("sub-ada", "ws-fixture-1", "user-ada")}),
			// Another user of the same workspace.
			taken: loginFile("codex-ada.json", "codex", map[string]any{"type": "codex", "access_token": "fixture-c", "account_id": "ws-fixture-1", "id_token": provimuxFixtureIDToken("sub-bo", "ws-fixture-1", "user-bo")}),
		},
		{
			name: "Copilot", basis: "a", sameAs: true, follow: true,
			first: loginFile("copilot-ada.json", "copilot", map[string]any{"type": "copilot", "access_token": "fixture-a", "account_id": "40001", "username": "fixture-ada"}),
			twin:  loginFile("copilot-ada-copy.json", "copilot", map[string]any{"type": "copilot", "access_token": "fixture-b", "account_id": "40001", "username": "fixture-ada"}),
			taken: loginFile("copilot-ada.json", "copilot", map[string]any{"type": "copilot", "access_token": "fixture-c", "account_id": "40002", "username": "fixture-bo"}),
		},
		{
			name: "Devin", basis: "a", sameAs: true, follow: true,
			first: loginFile("devin-ada.json", "devin", map[string]any{"type": "devin", "access_token": "fixture-a", "org_id": "org-fixture-1", "user_id": "user-fixture-1"}),
			twin:  loginFile("devin-ada-copy.json", "devin", map[string]any{"type": "devin", "access_token": "fixture-b", "org_id": "org-fixture-1", "user_id": "user-fixture-1"}),
			taken: loginFile("devin-ada.json", "devin", map[string]any{"type": "devin", "access_token": "fixture-c", "org_id": "org-fixture-1", "user_id": "user-fixture-2"}),
		},
		{
			name: "xAI login", basis: "a", sameAs: true, follow: true,
			first: loginFile("xai-ada.json", "xai", map[string]any{"type": "xai", "access_token": "fixture-a", "sub": "sub-fixture-1"}),
			twin:  loginFile("xai-ada-copy.json", "xai", map[string]any{"type": "xai", "access_token": "fixture-b", "sub": "sub-fixture-1"}),
			taken: loginFile("xai-ada.json", "xai", map[string]any{"type": "xai", "access_token": "fixture-c", "sub": "sub-fixture-2"}),
		},
		{
			name: "Vertex service account", basis: "a", sameAs: true, follow: true,
			first: loginFile("vertex-ada.json", "vertex", map[string]any{"type": "vertex", "service_account": map[string]any{"client_email": "runner@fixture-project.iam.example.test"}}),
			twin:  loginFile("vertex-ada-copy.json", "vertex", map[string]any{"type": "vertex", "service_account": map[string]any{"client_email": "runner@fixture-project.iam.example.test"}}),
			taken: loginFile("vertex-ada.json", "vertex", map[string]any{"type": "vertex", "service_account": map[string]any{"client_email": "other@fixture-project.iam.example.test"}}),
		},
		{
			name: "Meta login", basis: "a", sameAs: true, follow: true,
			first: loginFile("meta-ada.json", "meta", map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "fixture-a", "email": "ada@fixture.example.test"}),
			twin:  loginFile("meta-ada-copy.json", "meta", map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "fixture-b", "email": "ada@fixture.example.test"}),
			taken: loginFile("meta-ada.json", "meta", map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "fixture-c", "email": "bo@fixture.example.test"}),
		},
		{
			name: "Meta login whose vendor answer named no e-mail", basis: "s", sameAs: false, follow: false,
			first: loginFile("meta-cy.json", "meta", map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "fixture-a"}),
			twin:  loginFile("meta-cy-copy.json", "meta", map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "fixture-b"}),
			taken: loginFile("meta-cy.json", "meta", map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "fixture-c"}),
		},
		{
			name: "Antigravity", basis: "a", sameAs: true, follow: true,
			first: loginFile("antigravity-ada.json", "antigravity", map[string]any{"type": "antigravity", "access_token": "fixture-a", "email": "ada@fixture.example.test"}),
			twin:  loginFile("antigravity-ada-copy.json", "antigravity", map[string]any{"type": "antigravity", "access_token": "fixture-b", "email": "ada@fixture.example.test"}),
			taken: loginFile("antigravity-ada.json", "antigravity", map[string]any{"type": "antigravity", "access_token": "fixture-c", "email": "bo@fixture.example.test"}),
		},
		{
			// Two logins of one account have two device ids: unequal, and the replaced login changes.
			name: "Kimi login", basis: "s", sameAs: false, follow: true,
			first: loginFile("kimi-ada.json", "kimi", map[string]any{"type": "kimi", "access_token": "fixture-a", "device_id": "device-fixture-1"}),
			twin:  loginFile("kimi-ada-copy.json", "kimi", map[string]any{"type": "kimi", "access_token": "fixture-b", "device_id": "device-fixture-2"}),
			taken: loginFile("kimi-ada.json", "kimi", map[string]any{"type": "kimi", "access_token": "fixture-c", "device_id": "device-fixture-3"}),
		},
		{
			// One key in two rows of the configuration; the row taken by another key is another account.
			name: "API key with a base URL", basis: "a", sameAs: true, follow: true,
			first: configKey("claude-key-1", "claude", "sk-fixture-one", "https://gateway.example.test/v1", 0),
			twin:  configKey("claude-key-1-again", "claude", "sk-fixture-one", "HTTPS://Gateway.Example.Test/v1/", 3),
			taken: configKey("claude-key-1", "claude", "sk-fixture-two", "https://gateway.example.test/v1", 0),
		},
		{
			name: "API key without a base URL", basis: "a", sameAs: true, follow: true,
			first: configKey("gemini-key-1", "gemini", "fixture-gemini-one", "", 0),
			twin:  configKey("gemini-key-1-again", "gemini", "fixture-gemini-one", "", 1),
			taken: configKey("gemini-key-1", "gemini", "fixture-gemini-two", "", 0),
		},
		{
			name: "openai-compatibility entry without a key", basis: "a", sameAs: true, follow: true,
			first: configKey("compat-house", "openai-compatible-house", "", "https://llm.example.test/v1", 0),
			twin:  configKey("compat-house-again", "openai-compatible-house", "", "https://llm.example.test/v1", 2),
			taken: configKey("compat-house", "openai-compatible-house", "", "https://other-llm.example.test/v1", 0),
		},
		{
			name: "AI Studio (a browser's connection)", basis: "s", sameAs: false, follow: false,
			first: &Auth{ID: "aistudio-channel-1", Provider: "aistudio", Attributes: map[string]string{AttributeRuntimeOnly: "true"}, Metadata: map[string]any{"email": "aistudio-channel-1"}},
			twin:  &Auth{ID: "aistudio-channel-2", Provider: "aistudio", Attributes: map[string]string{AttributeRuntimeOnly: "true"}, Metadata: map[string]any{"email": "aistudio-channel-2"}},
			taken: &Auth{ID: "aistudio-channel-1", Provider: "aistudio", Attributes: map[string]string{AttributeRuntimeOnly: "true"}, Metadata: map[string]any{"email": "aistudio-channel-1"}},
		},
		{
			name: "a credential a plugin's parser made", basis: "s", sameAs: false, follow: false,
			first: func() *Auth {
				a := loginFile("plugin-ada.json#0", "fixture-plugin", map[string]any{"email": "ada@fixture.example.test", "access_token": "fixture-a"})
				MarkPluginVirtualAuth(a, "/fixture/auths/plugin-ada.json", 0)
				return a
			}(),
			twin: func() *Auth {
				a := loginFile("plugin-ada.json#1", "fixture-plugin", map[string]any{"email": "ada@fixture.example.test", "access_token": "fixture-b"})
				MarkPluginVirtualAuth(a, "/fixture/auths/plugin-ada.json", 1)
				return a
			}(),
			taken: func() *Auth {
				a := loginFile("plugin-ada.json#0", "fixture-plugin", map[string]any{"email": "bo@fixture.example.test", "access_token": "fixture-c"})
				MarkPluginVirtualAuth(a, "/fixture/auths/plugin-ada.json", 0)
				return a
			}(),
		},
		{
			// A login of a kind the table does not name (here the legacy Gemini CLI login): the credential's.
			name: "a login of a kind without a vendor identity in the table", basis: "s", sameAs: false, follow: false,
			first: loginFile("gemini-ada.json", "gemini-cli", map[string]any{"type": "gemini", "access_token": "fixture-a", "email": "ada@fixture.example.test"}),
			twin:  loginFile("gemini-ada-copy.json", "gemini-cli", map[string]any{"type": "gemini", "access_token": "fixture-b", "email": "ada@fixture.example.test"}),
			taken: loginFile("gemini-ada.json", "gemini-cli", map[string]any{"type": "gemini", "access_token": "fixture-c", "email": "bo@fixture.example.test"}),
		},
	}
}

func TestProvimuxAccountMarkerPerKind(t *testing.T) {
	withProvimuxMarkerKey(t, provimuxTestMarkerKey)
	seen := map[string]string{}
	for _, row := range provimuxMarkerRows() {
		t.Run(row.name, func(t *testing.T) {
			first, twin, taken := markerOf(t, row.first), markerOf(t, row.twin), markerOf(t, row.taken)
			if !strings.HasPrefix(first, row.basis+".") || !strings.HasPrefix(twin, row.basis+".") || !strings.HasPrefix(taken, row.basis+".") {
				t.Fatalf("basis: %s %s %s, want %q", first, twin, taken, row.basis)
			}
			if again := markerOf(t, row.first.Clone()); again != first {
				t.Fatalf("not the same for one credential: %s then %s", first, again)
			}
			if (twin == first) != row.sameAs {
				t.Fatalf("another credential of the account: %s vs %s, want equal %v", twin, first, row.sameAs)
			}
			if (taken != first) != row.follow {
				t.Fatalf("another login in the file: %s vs %s, want changed %v", taken, first, row.follow)
			}
			for _, marker := range []string{first, twin, taken} {
				if other, ok := seen[marker]; ok && other != row.name {
					t.Fatalf("%s gives %s, as %s does", row.name, marker, other)
				}
				seen[marker] = row.name
			}
		})
	}
}

// The identity is read from the login as it is at each use: nothing is remembered per credential. A Claude login
// whose file holds no ids yet is "s" until the executor writes them, and "a" after.
func TestProvimuxAccountMarkerFollowsTheLoginAsItIsNow(t *testing.T) {
	withProvimuxMarkerKey(t, provimuxTestMarkerKey)
	login := loginFile("claude-late.json", "claude", map[string]any{"type": "claude", "access_token": "fixture-a"})
	before := markerOf(t, login)
	if !strings.HasPrefix(before, "s.") {
		t.Fatalf("no ids yet: %s, want s.", before)
	}
	login.Metadata["organization_uuid"] = "0f0e0d0c-0b0a-4909-8807-060504030201"
	login.Metadata["account_uuid"] = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	after := markerOf(t, login)
	if !strings.HasPrefix(after, "a.") {
		t.Fatalf("ids written: %s, want a.", after)
	}
	login.Metadata["account_uuid"] = "2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e"
	if replaced := markerOf(t, login); replaced == after {
		t.Fatalf("another account's ids in the same credential keep %s", replaced)
	}
}

// Reordering the configuration's keys, renaming or moving a login file, and a token refresh leave the marker as it
// is: nothing in the hash is a position, a path or a token.
func TestProvimuxAccountMarkerIgnoresPositionPathAndTokens(t *testing.T) {
	withProvimuxMarkerKey(t, provimuxTestMarkerKey)
	key := configKey("codex-key", "codex", "sk-fixture-codex", "https://api.example.test/v1", 0)
	moved := configKey("codex-key-elsewhere", "codex", "sk-fixture-codex", "https://api.example.test/v1", 5)
	moved.Attributes["priority"] = "9"
	if markerOf(t, key) != markerOf(t, moved) {
		t.Fatal("a reordered key has another marker")
	}
	login := loginFile("antigravity-ada.json", "antigravity", map[string]any{"type": "antigravity", "access_token": "fixture-a", "refresh_token": "fixture-r", "email": "ada@fixture.example.test"})
	renamed := loginFile("subdir/renamed.json", "antigravity", map[string]any{"type": "antigravity", "access_token": "fixture-refreshed", "refresh_token": "fixture-r2", "email": "ada@fixture.example.test"})
	renamed.Index = "another-index"
	if markerOf(t, login) != markerOf(t, renamed) {
		t.Fatal("a renamed, refreshed login has another marker")
	}
}

// Two keys on one host differ; so do two paths of one host with one key, and the scheme; the host's case, a trailing
// slash and a fragment do not count.
func TestProvimuxAccountMarkerOfKeys(t *testing.T) {
	withProvimuxMarkerKey(t, provimuxTestMarkerKey)
	base := markerOf(t, configKey("k", "claude", "sk-fixture-one", "https://gateway.example.test/v1", 0))
	for name, other := range map[string]*Auth{
		"another key, same host": configKey("k2", "claude", "sk-fixture-two", "https://gateway.example.test/v1", 0),
		"another path":           configKey("k3", "claude", "sk-fixture-one", "https://gateway.example.test/v2", 0),
		"another scheme":         configKey("k4", "claude", "sk-fixture-one", "http://gateway.example.test/v1", 0),
		"another port":           configKey("k5", "claude", "sk-fixture-one", "https://gateway.example.test:8443/v1", 0),
		"another query":          configKey("k6", "claude", "sk-fixture-one", "https://gateway.example.test/v1?account=2", 0),
		"no base URL":            configKey("k7", "claude", "sk-fixture-one", "", 0),
	} {
		if marker := markerOf(t, other); marker == base {
			t.Errorf("%s: the same marker %s", name, marker)
		}
	}
	for name, same := range map[string]*Auth{
		"host in upper case": configKey("k8", "claude", "sk-fixture-one", "https://GATEWAY.example.test/v1", 0),
		"trailing slash":     configKey("k9", "claude", "sk-fixture-one", "https://gateway.example.test/v1/", 0),
		"a fragment":         configKey("k10", "claude", "sk-fixture-one", "https://gateway.example.test/v1#x", 0),
		"another family":     configKey("k11", "openai-compatible-gw", "sk-fixture-one", "https://gateway.example.test/v1", 0),
	} {
		if marker := markerOf(t, same); marker != base {
			t.Errorf("%s: %s, want %s", name, marker, base)
		}
	}
	// A key the configuration did not make (a plugin's, one added at run time) is still a key.
	runtimeKey := &Auth{ID: "runtime-key", Provider: "fixture-plugin", Attributes: map[string]string{AttributeAPIKey: "sk-fixture-one", "base_url": "https://gateway.example.test/v1"}}
	if marker := markerOf(t, runtimeKey); marker != base {
		t.Errorf("a key outside the configuration: %s, want %s", marker, base)
	}
}

// Without a key no marker is made; under another installation's key every marker is another; a field holding a NUL
// byte names the credential only; the marker holds neither the key nor any identity.
func TestProvimuxAccountMarkerKeyAndSecrecy(t *testing.T) {
	rows := provimuxMarkerRows()
	withProvimuxMarkerKey(t, nil)
	for _, row := range rows {
		if marker := row.first.ProvimuxAccountMarker(); marker != "" {
			t.Fatalf("%s without a key: %q", row.name, marker)
		}
	}
	SetProvimuxMarkerKey(provimuxTestMarkerKey)
	mine := map[string]string{}
	for _, row := range rows {
		mine[row.name] = markerOf(t, row.first)
	}
	SetProvimuxMarkerKey([]byte("another-installation-key-32bytes"))
	for _, row := range rows {
		if theirs := markerOf(t, row.first); theirs == mine[row.name] {
			t.Errorf("%s: the same marker under another installation's key", row.name)
		}
	}
	SetProvimuxMarkerKey(provimuxTestMarkerKey)
	nul := loginFile("devin-nul.json", "devin", map[string]any{"type": "devin", "org_id": "org-fixture-1\x00user-fixture-1", "user_id": "x"})
	glued := loginFile("devin-glued.json", "devin", map[string]any{"type": "devin", "org_id": "org-fixture-1", "user_id": "user-fixture-1\x00x"})
	if a, b := markerOf(t, nul), markerOf(t, glued); !strings.HasPrefix(a, "s.") || !strings.HasPrefix(b, "s.") || a == b {
		t.Fatalf("fields with a NUL byte: %s %s, want two s. markers", a, b)
	}
	if marker := (&Auth{Provider: "devin", Metadata: map[string]any{"user_id": "x\x00"}}).ProvimuxAccountMarker(); marker != "" {
		t.Fatalf("no vendor identity and no id: %q, want none", marker)
	}
	if marker := (*Auth)(nil).ProvimuxAccountMarker(); marker != "" {
		t.Fatalf("nil credential: %q", marker)
	}
	logging.SetProvimuxAnsweredBy(false) // models.answered-by: false
	for _, row := range rows {
		if marker := row.first.ProvimuxAccountMarker(); marker != "" {
			t.Fatalf("%s with the switch off: %q", row.name, marker)
		}
	}
	logging.SetProvimuxAnsweredBy(true)
	for _, row := range rows {
		marker := mine[row.name]
		for _, secret := range []string{string(provimuxTestMarkerKey), row.first.ID, "fixture", "example"} {
			if strings.Contains(marker, secret) {
				t.Fatalf("%s: marker %s holds %q", row.name, marker, secret)
			}
		}
	}
}
