package auth

import (
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

const (
	AuthKindAPIKey = "apikey"
	AuthKindOAuth  = "oauth"

	AuthSourceConfig      = "config"
	AuthSourceFile        = "file"
	AuthSourceGit         = "git"
	AuthSourceMemory      = "memory"
	AuthSourceObjectStore = "objectstore"
	AuthSourcePostgres    = "postgres"

	AttributeAPIKey               = "api_key"
	AttributeAuthKind             = "auth_kind"
	AttributeCodexAlphaSearch     = "codex_alpha_search"
	AttributeCodexDisableCloaking = "codex_disable_cloaking"
	AttributeConfigIndex          = "config_index"
	AttributePath                 = "path"
	AttributeRuntimeOnly          = "runtime_only"
	AttributeSource               = "source"
	AttributeSourceBackend        = "source_backend"
	AttributeWeight               = "weight"
)

// AuthKind returns the credential kind using explicit metadata first and legacy
// field-shape fallbacks second.
func (a *Auth) AuthKind() string {
	if a == nil {
		return ""
	}
	if IsCodexUseSIWC(a) {
		return AuthKindOAuth
	}
	if kind := normalizeAuthKind(authAttribute(a, AttributeAuthKind)); kind != "" {
		return kind
	}
	if kind := normalizeAuthKind(authMetadataString(a, AttributeAuthKind)); kind != "" {
		return kind
	}
	if authAttribute(a, AttributeAPIKey) != "" {
		return AuthKindAPIKey
	}
	if authHasOAuthMetadata(a) {
		return AuthKindOAuth
	}
	return ""
}

// BaseURLHost returns the lowercased host of an API key's base URL, or "" for a login or a key without one. Only the
// host: no credentials, port, path or query, since a path can carry an account ID. The model accounts list and the
// X-CPA-AUTH-HOST response header both use it, so they cannot disagree.
func (a *Auth) BaseURLHost() string {
	if a == nil || a.Attributes == nil || a.AuthKind() != AuthKindAPIKey {
		return ""
	}
	parsed, errParse := url.Parse(strings.TrimSpace(a.Attributes["base_url"]))
	if errParse != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// AuthStatement returns, as one statement, what names this credential to a client: its kind, its provider and, for
// an API key with a base URL, the host (BaseURLHost).
func (a *Auth) AuthStatement() logging.AuthStatement {
	if a == nil {
		return logging.AuthStatement{}
	}
	return logging.AuthStatement{Kind: a.AuthKind(), Provider: a.Provider, Host: a.BaseURLHost()}
}

// AuthSourceKind returns where the Auth entry came from at runtime.
func (a *Auth) AuthSourceKind() string {
	if a == nil {
		return ""
	}
	if strings.EqualFold(authAttribute(a, AttributeRuntimeOnly), "true") {
		return AuthSourceMemory
	}
	if source := normalizeAuthSourceKind(authAttribute(a, AttributeSourceBackend)); source != "" {
		return source
	}
	source := authAttribute(a, AttributeSource)
	if source != "" {
		sourceLower := strings.ToLower(source)
		if strings.HasPrefix(sourceLower, AuthSourceConfig+":") {
			return AuthSourceConfig
		}
		if normalized := normalizeAuthSourceKind(source); normalized != "" {
			return normalized
		}
		return AuthSourceFile
	}
	if authAttribute(a, AttributePath) != "" {
		return AuthSourceFile
	}
	if strings.TrimSpace(a.FileName) != "" {
		return AuthSourceFile
	}
	return ""
}

func normalizeAuthKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case AuthKindAPIKey, "api_key", "api-key":
		return AuthKindAPIKey
	case AuthKindOAuth, "oauth2":
		return AuthKindOAuth
	default:
		return ""
	}
}

func normalizeAuthSourceKind(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case AuthSourceConfig:
		return AuthSourceConfig
	case AuthSourceFile, "filesystem":
		return AuthSourceFile
	case AuthSourceGit:
		return AuthSourceGit
	case AuthSourceMemory, "runtime", "runtime_only":
		return AuthSourceMemory
	case AuthSourceObjectStore, "object-store":
		return AuthSourceObjectStore
	case AuthSourcePostgres, "postgresql", "database", "db":
		return AuthSourcePostgres
	default:
		return ""
	}
}

func authHasOAuthMetadata(auth *Auth) bool {
	if auth == nil || len(auth.Metadata) == 0 {
		return false
	}
	for _, key := range []string{"access_token", "refresh_token", "id_token", "email", "token_type", "expires_at", "expired"} {
		if authMetadataString(auth, key) != "" {
			return true
		}
	}
	if token, ok := auth.Metadata["token"].(map[string]any); ok && len(token) > 0 {
		return true
	}
	if siwc, ok := auth.Metadata["siwc"].(map[string]any); ok && len(siwc) > 0 {
		return true
	}
	if sa, ok := auth.Metadata["service_account"].(map[string]any); ok && len(sa) > 0 {
		return true
	}
	return false
}

func authAttribute(auth *Auth, key string) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[key])
}

func authMetadataString(auth *Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	switch value := auth.Metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}
