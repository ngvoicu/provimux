package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPublishSelectedAuthMetadataIncludesStableIndex(t *testing.T) {
	auth := &Auth{
		ID:       "auth-1",
		Provider: "codex",
		FileName: "auth-1.json",
	}
	selectedAuthID := ""
	selectedAuthIndex := ""
	meta := map[string]any{
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			selectedAuthID = authID
		},
		cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey: func(authIndex string) {
			selectedAuthIndex = authIndex
		},
	}

	publishSelectedAuthMetadata(meta, auth)

	if selectedAuthID != auth.ID {
		t.Fatalf("selected auth ID = %q, want %q", selectedAuthID, auth.ID)
	}
	if selectedAuthIndex == "" || selectedAuthIndex != auth.Index {
		t.Fatalf("selected auth index = %q, want %q", selectedAuthIndex, auth.Index)
	}
	if got := meta[cliproxyexecutor.SelectedAuthMetadataKey]; got != auth.ID {
		t.Fatalf("selected auth metadata = %#v, want %q", got, auth.ID)
	}
	if got := meta[cliproxyexecutor.SelectedAuthIndexMetadataKey]; got != auth.Index {
		t.Fatalf("selected auth index metadata = %#v, want %q", got, auth.Index)
	}
}

// The kind is published once a credential has answered; selecting one clears it.
func TestPublishAnsweredAuthMetadataReportsCredentialKind(t *testing.T) {
	for _, test := range []struct {
		name string
		auth *Auth
		want string
	}{
		{name: "api key", auth: &Auth{ID: "key-1", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "k"}}, want: AuthKindAPIKey},
		{name: "login", auth: &Auth{ID: "login-1", Provider: "claude", Attributes: map[string]string{AttributeAuthKind: "oauth"}}, want: AuthKindOAuth},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind := "not called"
			meta := map[string]any{
				cliproxyexecutor.AnsweredAuthCallbackMetadataKey: func(value, _, _ string) { kind = value },
			}
			publishSelectedAuthMetadata(meta, test.auth)
			if kind != "" {
				t.Fatalf("kind after selection = %q, want none until the credential answers", kind)
			}
			publishAnsweredAuthMetadata(meta, test.auth)
			if kind != test.want {
				t.Fatalf("answered auth kind = %q, want %q", kind, test.want)
			}
		})
	}
}

// The provider and host are published once a credential has answered; selecting one clears them.
func TestPublishAnsweredAuthMetadataReportsCredentialProviderAndHost(t *testing.T) {
	for _, test := range []struct {
		name     string
		auth     *Auth
		provider string
		host     string
	}{
		{name: "api key with a base URL", auth: &Auth{ID: "key-1", Provider: "openai-compatible-lab", Attributes: map[string]string{AttributeAPIKey: "k", "base_url": "https://user:pass@API.Example.test:8443/acct-1/v1?token=t"}}, provider: "openai-compatible-lab", host: "api.example.test"},
		{name: "api key without a base URL", auth: &Auth{ID: "key-2", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "k"}}, provider: "claude"},
		{name: "login", auth: &Auth{ID: "login-1", Provider: "devin", Attributes: map[string]string{AttributeAuthKind: "oauth", "base_url": "https://login.example.test/acct-1"}}, provider: "devin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, host := "not called", "not called"
			meta := map[string]any{
				cliproxyexecutor.AnsweredAuthCallbackMetadataKey: func(_, providerValue, hostValue string) { provider, host = providerValue, hostValue },
			}
			publishSelectedAuthMetadata(meta, test.auth)
			if provider != "" || host != "" {
				t.Fatalf("provider, host after selection = %q, %q, want none until the credential answers", provider, host)
			}
			publishAnsweredAuthMetadata(meta, test.auth)
			if provider != test.provider {
				t.Fatalf("answered auth provider = %q, want %q", provider, test.provider)
			}
			// Called with "" when there is no host, so a retry clears the host of an earlier credential.
			if host != test.host {
				t.Fatalf("answered auth host = %q, want %q", host, test.host)
			}
			if got := test.auth.BaseURLHost(); got != test.host {
				t.Fatalf("BaseURLHost() = %q, want %q", got, test.host)
			}
		})
	}
}
