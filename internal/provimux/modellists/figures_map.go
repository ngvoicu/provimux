package modellists

// Which providers.dev entry an answer is (prices-and-cache.md 6.1; answered-by.md 4.2 and 4.3).
//
// An entry is <providers.dev provider>/<model id>. It is worked out from the request the gateway sent for an answer:
// the host the request went to, the names of its credential headers and the model id in it, read from the request as
// the transport was handed it; and from the credential that sent it: the gateway's provider of it (which executor
// sent) and its kind. The account's configured base URL, the registry and anything providers.dev sent are not read.
// Exact or nothing: what the map has no row for has no entry, and nothing is guessed from a model's name. No request:
// this is a map, the same with providers.dev off or unreachable. An entry says where the model would be, not that
// providers.dev has it.
//
// Keyed by the host that was sent: the "no base URL" alternatives are gone
// (a request always has a host), a login's own backends are rows of the map, and the signer joins the key.

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// The signers: which credential headers a request carries, by name only (answered-by.md 4.2).
const (
	SignerAPIKey        = "x-api-key"     // x-api-key and no Authorization
	SignerAuthorization = "authorization" // Authorization and no x-api-key
	SignerBoth          = "both"          // both
)

// entryHostProviders: a key's request to exactly one of these hosts is that providers.dev provider, whatever the
// gateway's own provider of the key (an OpenAI-compatible entry's name is the operator's choice). The ids are
// providers.dev's (its providers' base_url, site data of 2026-11-26).
var entryHostProviders = map[string]string{
	"openrouter.ai":    "openrouter",
	"api.deepseek.com": "deepseek",
	"api.moonshot.ai":  "moonshotai",
	"api.mistral.ai":   "mistral",
	"api.groq.com":     "groq",
	"api.cerebras.ai":  "cerebras",
	"api.fireworks.ai": "fireworks-ai",
	"api.together.ai":  "togetherai",
	// Together's API host as providers.dev itself states it.
	"api.together.xyz": "togetherai",
}

// antigravityHosts are the hosts an Antigravity login's own backend answers on.
var antigravityHosts = map[string]bool{
	"cloudcode-pa.googleapis.com":               true,
	"daily-cloudcode-pa.googleapis.com":         true,
	"daily-cloudcode-pa.sandbox.googleapis.com": true,
}

// openRouterRoutingSuffixes are OpenRouter's routing shortcuts: the same model, routed by price or speed. Any other
// ":" suffix (":free", ":online", ...) names something else and stays.
var openRouterRoutingSuffixes = []string{":nitro", ":floor"}

// EntryProvider returns the providers.dev provider of a request by the gateway's provider of the credential that sent
// it, the credential's kind ("oauth" or "apikey"), the lower-case host the request went to (over https on the default
// port: the caller's to check) and its signer (SignerAPIKey, SignerAuthorization, SignerBoth or ""), or "" where the
// map of prices-and-cache.md 6.1 has no row.
//
// The rows that answer "" on purpose: a Kimi login (its host decides between two plans), an xAI login (providers.dev's
// xai is the paid API), a Meta key (the paid API or a Muse Code subscription's key; neither the request nor the account
// says which), AI Studio, a Gemini or Gemini CLI login, a Gemini Interactions key, a Codex login's request to
// api.openai.com (the second sign-in: a subscription's token on the paid API's host), a Codex key's request to
// chatgpt.com, any request of a login to a host but its own backend's, and any request to a host the map does not name.
// The xAI row asks one more thing of the account (no using_api other than true): the caller's to check, it is not on
// the wire.
func EntryProvider(provider, kind, host, signer string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	kind = strings.ToLower(strings.TrimSpace(kind))
	host = strings.ToLower(strings.TrimSpace(host))
	signer = strings.ToLower(strings.TrimSpace(signer))
	if host == "" || (kind != "oauth" && kind != "apikey") {
		return ""
	}
	if kind == "apikey" {
		if entry, ok := entryHostProviders[host]; ok {
			return entry
		}
	}
	switch provider {
	case "claude":
		// The signer, not the kind: a login's token configured as a key is sent as a login's token.
		if host == "api.anthropic.com" {
			switch signer {
			case SignerAuthorization:
				return "claude-subscription"
			case SignerAPIKey:
				return "anthropic"
			}
		}
	case "codex":
		if kind == "oauth" && host == "chatgpt.com" {
			return "openai-codex"
		}
		if kind == "apikey" && host == "api.openai.com" {
			return "openai"
		}
	case "copilot":
		if kind == "oauth" && (host == "api.githubcopilot.com" || strings.HasSuffix(host, ".githubcopilot.com")) {
			return "github-copilot"
		}
	case "antigravity":
		if kind == "oauth" && antigravityHosts[host] {
			return "antigravity"
		}
	case "devin":
		if kind == "oauth" && host == "server.codeium.com" {
			return "devin"
		}
	case "meta":
		if kind == "oauth" && host == "api.meta.ai" {
			return "meta-subscription"
		}
	case "xai":
		if kind == "apikey" && host == "api.x.ai" {
			return "xai"
		}
	case "gemini":
		if kind == "apikey" && host == "generativelanguage.googleapis.com" {
			return "google"
		}
	case "vertex":
		if GoogleVertexHost(host) {
			return "google-vertex"
		}
	}
	return ""
}

// GoogleVertexHost reports whether host is a host the Vertex executor treats as Google's own: the executor's rule
// (helps.IsGoogleVertexHost), given the host alone, so its fallback for a base URL that does not parse never applies
// here. An empty host is none.
func GoogleVertexHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.ContainsAny(host, "/:@?#") {
		return false
	}
	return helps.IsGoogleVertexHost("https://" + host)
}

// EntryModel is the model id of an entry of provider for the id that was sent, or "" where there is none. Exact,
// except where the id names the same model another way: OpenRouter's routing suffix is taken off; a Devin request id
// (an effort variant, with or without devin/) is its model in Devin's catalog, and an id the catalog does not know has
// none. An id with spaces around it is not an id the vendor was sent as such: none. No dots for dashes, no dates
// removed.
func EntryModel(provider, sentModel string) string {
	id := strings.TrimSpace(sentModel)
	if id == "" || id != sentModel {
		return ""
	}
	switch provider {
	case "openrouter":
		for _, suffix := range openRouterRoutingSuffixes {
			if trimmed, ok := strings.CutSuffix(id, suffix); ok {
				id = trimmed
				break
			}
		}
	case "devin":
		info := registry.LookupDevinModel(id)
		if info == nil {
			return ""
		}
		id = strings.TrimSpace(info.ID)
		if len(id) > len("devin/") && strings.EqualFold(id[:len("devin/")], "devin/") {
			id = id[len("devin/"):]
		}
	}
	return id
}

// EntryRef joins a providers.dev provider and a model id as providers.dev writes a reference, or "" when either is
// empty. A provider never contains a slash; a model id may.
func EntryRef(provider, model string) string {
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if provider == "" || model == "" || strings.Contains(provider, "/") {
		return ""
	}
	return provider + "/" + model
}
