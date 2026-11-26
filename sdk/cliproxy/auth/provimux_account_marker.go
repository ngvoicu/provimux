package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// Provimux: the account marker (.github/provimux/design/answered-by.md sections 2.2 and 2.3). An opaque name of the
// account behind a credential: "a." or "s." and 20 characters of a keyed hash of the account's own identity at its
// vendor, under a key only this installation holds. It is the same for every credential of one account, another for
// every other account, the same after a restart or a reorder (the key is kept, nothing in the hash is a position or a
// path), and another when another account's login takes a file (the identity is read from the login as it is now, at
// every use). Neither the key nor any identity that goes into the hash is ever logged, returned or stored.

// provimuxMarkerContext is the fixed prefix of every hashed identity; a change of the rule is a new version here.
const provimuxMarkerContext = "provimux account marker v1"

const (
	// provimuxBasisAccount: the identity is the account as its vendor names it. Equal markers are one account at
	// the vendor, unequal ones are two.
	provimuxBasisAccount = "a"
	// provimuxBasisCredential: the identity is this gateway's credential, where the vendor's name for the account is
	// not held. Equal markers are one credential; unequal ones may still be one account.
	provimuxBasisCredential = "s"
)

// provimuxMarkerLength is the number of base32 characters of the hash in a marker: 100 bits.
const provimuxMarkerLength = 20

var provimuxMarkerEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// provimuxMarkerKey is the installation's key; nil until SetProvimuxMarkerKey gives one.
var provimuxMarkerKey atomic.Pointer[[]byte]

// SetProvimuxMarkerKey gives the key of the account markers (a copy is kept). Without one no marker is made and
// nothing is sent; an empty key removes it.
func SetProvimuxMarkerKey(key []byte) {
	if len(key) == 0 {
		provimuxMarkerKey.Store(nil)
		return
	}
	kept := append([]byte(nil), key...)
	provimuxMarkerKey.Store(&kept)
}

// ProvimuxAccountMarker returns the account's marker (answered-by.md 2.2 and 2.3), or "" with models.answered-by
// off, without a key, or for a credential that has no statement.
func (a *Auth) ProvimuxAccountMarker() string {
	if !logging.ProvimuxAnsweredBy() {
		return ""
	}
	key := provimuxMarkerKey.Load()
	if key == nil || len(*key) == 0 {
		return ""
	}
	basis, identity := provimuxAccountIdentity(a)
	if basis == "" {
		return ""
	}
	return provimuxMarkerOf(*key, basis, identity)
}

// provimuxMarkerOf is the rule of answered-by.md 2.2: the basis, a dot, and the first 20 base32 characters of the
// HMAC-SHA-256 under key of the context, the basis and the identity. The basis is inside the hash too, so an "a" and
// an "s" identity of the same bytes never share their 20 characters.
func provimuxMarkerOf(key []byte, basis string, identity []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(provimuxMarkerContext))
	mac.Write([]byte{0})
	mac.Write([]byte(basis))
	mac.Write([]byte{0})
	mac.Write(identity)
	return basis + "." + strings.ToLower(provimuxMarkerEncoding.EncodeToString(mac.Sum(nil)))[:provimuxMarkerLength]
}

// provimuxAccountIdentity returns the basis ("a" or "s") and the bytes hashed for a: a kind tag, then the kind's
// fields, each ended by a NUL byte. "" for no credential. Where the vendor's own name for the account is not held, or
// is not known to be the vendor's, the basis is "s" and the identity is the credential's id: an "s" marker can say
// "not the same" of one account, never "the same" of two.
func provimuxAccountIdentity(a *Auth) (basis string, identity []byte) {
	if a == nil {
		return "", nil
	}
	credential := func() (string, []byte) {
		return provimuxIdentity(provimuxBasisCredential, "credential", strings.TrimSpace(a.ID))
	}
	if basis, identity = provimuxVendorIdentity(a, credential); basis == "" {
		// A field that could make two identities one (a NUL byte): the credential's own.
		return credential()
	}
	return basis, identity
}

func provimuxVendorIdentity(a *Auth, credential func() (string, []byte)) (string, []byte) {
	// A browser's connection (AI Studio), any credential held only in memory, and any credential a plugin made
	// (whatever provider it names and however many its parser returned): the gateway's own credential. Fields plugin
	// code wrote are not the vendor's own word.
	if strings.EqualFold(authAttribute(a, AttributeRuntimeOnly), "true") || IsPluginVirtualAuth(a) || provimuxPluginMade(a) {
		return credential()
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	// An API key of the configuration, of any family: another key is another account.
	if strings.HasPrefix(strings.ToLower(authAttribute(a, AttributeSource)), AuthSourceConfig+":") {
		return provimuxKeyIdentity(a, provider)
	}
	switch provider {
	case "claude":
		return provimuxClaudeIdentity(a, credential)
	case "codex":
		return provimuxCodexIdentity(a, credential)
	case "copilot":
		if id := authMetadataString(a, "account_id"); id != "" {
			return provimuxIdentity(provimuxBasisAccount, "copilot", id)
		}
	case "devin":
		// A user of Devin belongs to an organisation or to none; the user id is what names the account.
		if user := authMetadataString(a, "user_id"); user != "" {
			return provimuxIdentity(provimuxBasisAccount, "devin", authMetadataString(a, "org_id"), user)
		}
	case "xai":
		if sub := authMetadataString(a, "sub"); sub != "" {
			return provimuxIdentity(provimuxBasisAccount, "xai", sub)
		}
	case "vertex":
		if sa, ok := a.Metadata["service_account"].(map[string]any); ok {
			if email, okEmail := sa["client_email"].(string); okEmail && strings.TrimSpace(email) != "" {
				return provimuxIdentity(provimuxBasisAccount, "vertex", strings.TrimSpace(email))
			}
		}
	case "meta":
		// Meta's login holds no account id: the e-mail its vendor names at every mint (meta_executor.go).
		if email := authMetadataString(a, "email"); email != "" {
			return provimuxIdentity(provimuxBasisAccount, "meta", email)
		}
	case "antigravity":
		// The Google account's e-mail is the only account field the login holds.
		if email := authMetadataString(a, "email"); email != "" {
			return provimuxIdentity(provimuxBasisAccount, "antigravity", email)
		}
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com": // every name the loader accepts for a Kimi login
		// Its device id is made anew at each login: another login in the file gives another marker, and two
		// logins of one account give two.
		if device := authMetadataString(a, "device_id"); device != "" {
			return provimuxIdentity(provimuxBasisCredential, "kimi", device)
		}
	default:
		// A key that is not of the configuration (one added at run time) is still a key. A plugin's is not here.
		if a.AuthKind() == AuthKindAPIKey && authAttribute(a, AttributeAPIKey) != "" {
			return provimuxKeyIdentity(a, provider)
		}
	}
	return credential()
}

// provimuxClaudeIdentity is a Claude login's: its organisation and account, as Anthropic named them. Without an
// organisation id, or with an account id the gateway made up itself (a version-5 UUID of the credential, written for
// a setup token or a profile the vendor refused: claude_executor_auth.go), it is the credential's.
func provimuxClaudeIdentity(a *Auth, credential func() (string, []byte)) (string, []byte) {
	if a.AuthKind() == AuthKindAPIKey && authAttribute(a, AttributeAPIKey) != "" {
		return provimuxKeyIdentity(a, "claude")
	}
	organisation := authMetadataString(a, "organization_uuid")
	account := authMetadataString(a, "account_uuid")
	if account == "" {
		account = authMetadataString(a, "accountUuid")
	}
	if organisation == "" || account == "" {
		return credential()
	}
	if parsed, errParse := uuid.Parse(account); errParse == nil && parsed.Version() == 5 {
		return credential()
	}
	return provimuxIdentity(provimuxBasisAccount, "claude", organisation, account)
}

// provimuxCodexIdentity is a Codex login's: the ChatGPT workspace (account_id) and the user its id token names. A
// workspace holds several users, so without a user the identity is the credential's.
func provimuxCodexIdentity(a *Auth, credential func() (string, []byte)) (string, []byte) {
	if a.AuthKind() == AuthKindAPIKey && authAttribute(a, AttributeAPIKey) != "" {
		return provimuxKeyIdentity(a, "codex")
	}
	workspace := authMetadataString(a, "account_id")
	user := ""
	if token := authMetadataString(a, "id_token"); token != "" {
		if claims, errParse := codex.ParseJWTToken(token); errParse == nil && claims != nil {
			if workspace == "" {
				workspace = strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID)
			}
			for _, candidate := range []string{claims.CodexAuthInfo.ChatgptUserID, claims.CodexAuthInfo.UserID, claims.Sub} {
				if user = strings.TrimSpace(candidate); user != "" {
					break
				}
			}
		}
	}
	if workspace == "" || user == "" {
		return credential()
	}
	return provimuxIdentity(provimuxBasisAccount, "codex", workspace, user)
}

// provimuxKeyIdentity is an API key's: its base URL (scheme and host in lower case, the path without a trailing
// slash) and the key; without a base URL, the provider's name and the key. An entry without a key (an
// openai-compatibility entry) is its base URL alone.
func provimuxKeyIdentity(a *Auth, provider string) (string, []byte) {
	key := authAttribute(a, AttributeAPIKey)
	if base := authAttribute(a, "base_url"); base != "" {
		return provimuxIdentity(provimuxBasisAccount, "apikey", "url", provimuxBaseURLIdentity(base), key)
	}
	if key == "" {
		return provimuxIdentity(provimuxBasisCredential, "credential", strings.TrimSpace(a.ID))
	}
	return provimuxIdentity(provimuxBasisAccount, "apikey", "provider", provider, key)
}

// provimuxBaseURLIdentity writes a base URL as one account's address: scheme and host in lower case, user info kept,
// the port kept unless it is the scheme's default (443 for https, 80 for http), the path without its trailing slashes,
// the query kept, the fragment dropped. A base URL that does not parse to a host is taken as written.
func provimuxBaseURLIdentity(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Host == "" {
		return raw
	}
	var out strings.Builder
	out.WriteString(strings.ToLower(parsed.Scheme))
	out.WriteString("://")
	if parsed.User != nil {
		out.WriteString(parsed.User.String())
		out.WriteByte('@')
	}
	host := strings.ToLower(parsed.Host)
	if port := parsed.Port(); port == "443" && strings.EqualFold(parsed.Scheme, "https") || port == "80" && strings.EqualFold(parsed.Scheme, "http") {
		host = strings.TrimSuffix(host, ":"+port)
	}
	out.WriteString(host)
	out.WriteString(strings.TrimRight(parsed.EscapedPath(), "/"))
	if parsed.RawQuery != "" {
		out.WriteByte('?')
		out.WriteString(parsed.RawQuery)
	}
	return out.String()
}

// provimuxIdentity joins a kind tag and its fields, each ended by a NUL byte. A field that holds a NUL byte itself
// could make two identities one: it gives no identity (the caller falls back to the credential's id), and neither
// does a credential without an id.
func provimuxIdentity(basis, tag string, fields ...string) (string, []byte) {
	size := len(tag) + 1
	for _, field := range fields {
		if strings.IndexByte(field, 0) >= 0 {
			return "", nil
		}
		size += len(field) + 1
	}
	if basis == provimuxBasisCredential && (len(fields) == 0 || fields[len(fields)-1] == "") {
		return "", nil
	}
	identity := make([]byte, 0, size)
	identity = append(identity, tag...)
	identity = append(identity, 0)
	for _, field := range fields {
		identity = append(identity, field...)
		identity = append(identity, 0)
	}
	return basis, identity
}

// provimuxPluginMade reports whether a plugin made the credential: its storage is the plugin host's, which says so.
// The storage pointer survives Clone, so the lists and the answers agree.
func provimuxPluginMade(a *Auth) bool {
	made, ok := a.Storage.(interface{ ProvimuxPluginMade() bool })
	return ok && made.ProvimuxPluginMade()
}
