package auth

import (
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"golang.org/x/net/idna"
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

// baseURLHost returns the host of an API key's base URL in ASCII, lower case: only the host (no credentials, port,
// path or query, since a path can carry an account ID); an internationalised host in its IDNA form
// (bücher.example: xn--bcher-kva.example). ok is false when there is a base URL whose host cannot be given: it does
// not parse, has no host (a URL without a scheme), or does not convert. A login, or a key without a base URL, has no
// host and ok is true.
func (a *Auth) baseURLHost() (host string, ok bool) {
	if a == nil || a.Attributes == nil || a.AuthKind() != AuthKindAPIKey {
		return "", true
	}
	raw := strings.TrimSpace(a.Attributes["base_url"])
	if raw == "" {
		return "", true
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil {
		return "", false
	}
	host = strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", false
	}
	if !isASCII(host) {
		// An ASCII host is not passed through IDNA: its strict profile rejects valid names (my_gateway.internal, ::1).
		converted, errConvert := idna.Lookup.ToASCII(host)
		if errConvert != nil {
			return "", false
		}
		host = converted
	}
	for i := 0; i < len(host); i++ {
		if host[i] < 0x21 || host[i] > 0x7e {
			return "", false
		}
	}
	return host, true
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

// BaseURLHost returns the host of an API key's base URL in ASCII (see baseURLHost), or "" for a login, a key without
// one, or a base URL whose host cannot be given. The model accounts list and the X-CPA-AUTH-HOST response header both
// use it, so they cannot disagree.
func (a *Auth) BaseURLHost() string {
	host, _ := a.baseURLHost()
	return host
}

// AuthStatement returns, as one statement, what names this credential to a client: its kind, its provider and, for
// an API key with a base URL, the host (BaseURLHost). It is the zero statement for a key whose base URL has no host
// that can be given: such a key is never stated as a key without a base URL.
func (a *Auth) AuthStatement() logging.AuthStatement {
	if a == nil {
		return logging.AuthStatement{}
	}
	host, ok := a.baseURLHost()
	if !ok {
		return logging.AuthStatement{}
	}
	return logging.AuthStatement{Kind: a.AuthKind(), Provider: a.Provider, Host: host}
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
