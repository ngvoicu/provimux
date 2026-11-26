package pluginhost

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Provimux: a credential a plugin made is "s." however many its parser returned and whatever provider it names
// (answered-by.md 2.3): one credential a plugin calls claude, with Claude's ids, or antigravity, with an e-mail, is not
// named by those fields, and neither is a key it made. Its clone (what the lists read) agrees. Fixtures are invented.
func TestProvimuxPluginMadeCredentialIsTheGatewaysOwn(t *testing.T) {
	coreauth.SetProvimuxMarkerKey([]byte("provimux-plugin-made-test-key-32"))
	was := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() {
		logging.SetProvimuxAnsweredBy(was)
		coreauth.SetProvimuxMarkerKey(nil)
	})
	claudeIDs := map[string]any{"access_token": "from-the-plugin", "organization_uuid": "0f0e0d0c-0b0a-4909-8807-060504030201", "account_uuid": "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"}
	builtIn := &coreauth.Auth{ID: "claude-ada.json", Provider: "claude", Attributes: map[string]string{"path": "/fixture/auths/claude-ada.json"},
		Metadata: map[string]any{"type": "claude", "access_token": "fixture", "organization_uuid": claudeIDs["organization_uuid"], "account_uuid": claudeIDs["account_uuid"]}}
	if marker := builtIn.ProvimuxAccountMarker(); !strings.HasPrefix(marker, "a.") {
		t.Fatalf("fixture: the built-in Claude login is %q", marker)
	}
	for _, data := range []pluginapi.AuthData{
		{Provider: "claude", ID: "plug.json", Metadata: claudeIDs},
		{Provider: "antigravity", ID: "plug.json", Metadata: map[string]any{"access_token": "from-the-plugin", "email": "ada@fixture.example.test"}},
		{Provider: "fixture-plugin", ID: "plug.json", Attributes: map[string]string{"api_key": "sk-fixture-plugin", "base_url": "https://plug.example.test/v1"}},
	} {
		made := pluginAuthDataToCoreAuth(data, "/fixture/auths/plug.json", "plug.json", "/fixture/auths")
		if made == nil {
			t.Fatalf("%s: no credential", data.Provider)
		}
		marker := made.ProvimuxAccountMarker()
		if !strings.HasPrefix(marker, "s.") || marker != made.Clone().ProvimuxAccountMarker() || marker == builtIn.ProvimuxAccountMarker() {
			t.Fatalf("%s: one plugin-made credential is %q (clone %q), want s., the clone's, not the built-in login's", data.Provider, marker, made.Clone().ProvimuxAccountMarker())
		}
	}
}
