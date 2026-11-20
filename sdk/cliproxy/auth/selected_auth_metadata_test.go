package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
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

// The kind is published once a credential has answered; a selection publishes nothing.
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
			if kind != "not called" {
				t.Fatalf("a selection called the answered callback with kind %q", kind)
			}
			publishAnsweredAuthMetadata(upstreamAnswered(), meta, test.auth)
			if kind != test.want {
				t.Fatalf("answered auth kind = %q, want %q", kind, test.want)
			}
		})
	}
}

// The provider and host are published once a credential has answered; a selection publishes nothing.
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
			if provider != "not called" || host != "not called" {
				t.Fatalf("a selection called the answered callback with provider, host %q, %q", provider, host)
			}
			publishAnsweredAuthMetadata(upstreamAnswered(), meta, test.auth)
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

// The selected-kind callback of earlier releases keeps its calls: one per selection, with the selected credential's
// kind, also for a credential that then fails; under the exported constant and under its string alike.
func TestSelectedAuthKindCallbackIsCalledAtEverySelection(t *testing.T) {
	const model = "selected-kind-model"
	key := &Auth{ID: "kind-key", Provider: "claude", Status: StatusActive, Attributes: map[string]string{AttributeAPIKey: "made-up-key", "priority": "10"}}
	login := &Auth{ID: "kind-login", Provider: "devin", Status: StatusActive, Attributes: map[string]string{AttributeAuthKind: "oauth", "priority": "0"}}
	for _, metadataKey := range []string{cliproxyexecutor.SelectedAuthKindCallbackMetadataKey, "selected_auth_kind_callback"} {
		for _, operation := range []string{"plain", "count", "stream"} {
			t.Run(metadataKey+"/"+operation, func(t *testing.T) {
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 3)
				for _, credential := range []*Auth{key, login} {
					manager.RegisterExecutor(&answeredOnceExecutor{provider: credential.Provider, fail: map[string]bool{"kind-key": true}})
					if _, err := manager.Register(context.Background(), credential.Clone()); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
					id := credential.ID
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}
				var kinds []string
				opts := cliproxyexecutor.Options{Metadata: map[string]any{metadataKey: func(kind string) { kinds = append(kinds, kind) }}}
				providers, req := []string{"claude", "devin"}, cliproxyexecutor.Request{Model: model}
				var errExec error
				switch operation {
				case "plain":
					_, errExec = manager.Execute(context.Background(), providers, req, opts)
				case "count":
					_, errExec = manager.ExecuteCount(context.Background(), providers, req, opts)
				default:
					var stream *cliproxyexecutor.StreamResult
					stream, errExec = manager.ExecuteStream(context.Background(), providers, req, opts)
					if errExec == nil {
						for range stream.Chunks {
						}
					}
				}
				if errExec != nil {
					t.Fatalf("error = %v", errExec)
				}
				if got := strings.Join(kinds, ","); got != "apikey,oauth" {
					t.Fatalf("selected kinds = %q, want one call per selection: apikey (failed), oauth", got)
				}
			})
		}
	}
}
