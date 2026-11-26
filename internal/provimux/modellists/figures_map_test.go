package modellists

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// Every row of prices-and-cache.md 6.1, each "none" row included, keyed by what was sent: the provider of the
// credential that sent the request, its kind, the host the request went to and its signer. F1's matrix (50 cases,
// 19 providers), with each "no base URL" case now the host the executor sends to by default.
func TestEntryProviderEveryRowOfTheMap(t *testing.T) {
	const a, k, both = SignerAuthorization, SignerAPIKey, SignerBoth
	for _, tc := range []struct {
		row                          string
		provider, kind, host, signer string
		want                         string
	}{
		{"Claude login", "claude", "oauth", "api.anthropic.com", a, "claude-subscription"},
		{"Claude key", "claude", "apikey", "api.anthropic.com", k, "anthropic"},
		{"Claude key whose token is a login's: sent as a login's", "claude", "apikey", "api.anthropic.com", a, "claude-subscription"},
		{"Codex login", "codex", "oauth", "chatgpt.com", a, "openai-codex"},
		{"Codex key, api.openai.com", "codex", "apikey", "api.openai.com", a, "openai"},
		{"Copilot", "copilot", "oauth", "api.githubcopilot.com", a, "github-copilot"},
		{"Copilot, the host its login was given", "copilot", "oauth", "api.individual.githubcopilot.com", a, "github-copilot"},
		{"Copilot, an enterprise host", "copilot", "oauth", "api.enterprise.githubcopilot.com", a, "github-copilot"},
		{"Antigravity", "antigravity", "oauth", "cloudcode-pa.googleapis.com", a, "antigravity"},
		{"Antigravity, daily", "antigravity", "oauth", "daily-cloudcode-pa.googleapis.com", a, "antigravity"},
		{"Antigravity, daily sandbox", "antigravity", "oauth", "daily-cloudcode-pa.sandbox.googleapis.com", a, "antigravity"},
		{"Devin", "devin", "oauth", "server.codeium.com", "", "devin"},
		{"Meta login", "meta", "oauth", "api.meta.ai", a, "meta-subscription"},
		{"xAI key, api.x.ai", "xai", "apikey", "api.x.ai", a, "xai"},
		{"Gemini key", "gemini", "apikey", "generativelanguage.googleapis.com", "", "google"},
		{"Vertex service account", "vertex", "oauth", "aiplatform.googleapis.com", a, "google-vertex"},
		{"Vertex key, aiplatform.googleapis.com", "vertex", "apikey", "aiplatform.googleapis.com", "", "google-vertex"},
		{"Vertex key, a regional endpoint", "vertex", "apikey", "europe-west4-aiplatform.googleapis.com", "", "google-vertex"},
		{"Vertex key, a regional endpoint with a dot", "vertex", "apikey", "us-central1.aiplatform.googleapis.com", "", "google-vertex"},
		{"Vertex key, Google's US multi-region endpoint", "vertex", "apikey", "aiplatform.us.rep.googleapis.com", "", "google-vertex"},
		{"Vertex key, Google's EU multi-region endpoint", "vertex", "apikey", "aiplatform.eu.rep.googleapis.com", "", "google-vertex"},
		{"OpenRouter, an openai-compatibility entry", "provimux-test-mine", "apikey", "openrouter.ai", a, "openrouter"},
		{"OpenRouter, a Claude key on its host", "claude", "apikey", "openrouter.ai", k, "openrouter"},
		{"OpenRouter, a Vertex key on its host", "vertex", "apikey", "openrouter.ai", a, "openrouter"},
		{"api.deepseek.com", "openai-compatibility", "apikey", "api.deepseek.com", a, "deepseek"},
		{"api.moonshot.ai", "provimux-test-mine", "apikey", "api.moonshot.ai", a, "moonshotai"},
		{"api.mistral.ai", "provimux-test-mine", "apikey", "api.mistral.ai", a, "mistral"},
		{"api.groq.com", "provimux-test-mine", "apikey", "api.groq.com", a, "groq"},
		{"api.cerebras.ai", "provimux-test-mine", "apikey", "api.cerebras.ai", a, "cerebras"},
		{"api.fireworks.ai", "provimux-test-mine", "apikey", "api.fireworks.ai", a, "fireworks-ai"},
		{"api.together.ai", "provimux-test-mine", "apikey", "api.together.ai", a, "togetherai"},
		{"api.together.xyz", "provimux-test-mine", "apikey", "api.together.xyz", a, "togetherai"},
		{"case and spaces", " Claude ", "OAuth", " API.Anthropic.com ", " Authorization ", "claude-subscription"},

		// The rows that answer none.
		{"none: Claude, both credential headers", "claude", "apikey", "api.anthropic.com", both, ""},
		{"none: Claude, no credential header", "claude", "oauth", "api.anthropic.com", "", ""},
		{"none: Claude login on a relay", "claude", "oauth", "provimux-test-relay.invalid", a, ""},
		{"none: Kimi login", "kimi", "oauth", "api.kimi.com", a, ""},
		{"none: xAI login", "xai", "oauth", "api.x.ai", a, ""},
		{"none: xAI to the Grok CLI proxy", "xai", "apikey", "cli-chat-proxy.grok.com", a, ""},
		{"none: Meta key", "meta", "apikey", "api.meta.ai", a, ""},
		{"none: Meta login on a relay", "meta", "oauth", "provimux-test-relay.invalid", a, ""},
		{"none: AI Studio", "aistudio", "oauth", "generativelanguage.googleapis.com", "", ""},
		{"none: Gemini login", "gemini", "oauth", "generativelanguage.googleapis.com", a, ""},
		{"none: Gemini CLI login", "gemini-cli", "oauth", "cloudcode-pa.googleapis.com", a, ""},
		{"none: Interactions key on Google's host", "gemini-interactions", "apikey", "generativelanguage.googleapis.com", "", ""},
		{"none: Codex login's request to api.openai.com (the second sign-in)", "codex", "oauth", "api.openai.com", a, ""},
		{"none: Codex key's request to chatgpt.com", "codex", "apikey", "chatgpt.com", a, ""},
		{"none: Codex login on a relay", "codex", "oauth", "provimux-test-relay.invalid", a, ""},
		{"none: Copilot on a host that only ends like GitHub's", "copilot", "oauth", "githubcopilot.com.provimux-test.example", a, ""},
		{"none: Antigravity on a relay", "antigravity", "oauth", "provimux-test-relay.invalid", a, ""},
		{"none: Devin on a relay", "devin", "oauth", "provimux-test-relay.invalid", "", ""},
		{"none: Claude key on another host", "claude", "apikey", "provimux-test.example", k, ""},
		{"none: Codex key on another host", "codex", "apikey", "provimux-test.example", a, ""},
		{"none: xAI key on another host", "xai", "apikey", "provimux-test.example", a, ""},
		{"none: Gemini key on another host", "gemini", "apikey", "provimux-test.example", "", ""},
		{"none: Vertex-compatible key on another host", "vertex", "apikey", "provimux-test.example", "", ""},
		{"none: a host that only begins like Google's", "vertex", "apikey", "aiplatform.googleapis.com.provimux-test.example", "", ""},
		{"none: a REP host of two labels", "vertex", "apikey", "aiplatform.us.x.rep.googleapis.com", "", ""},
		{"none: a subdomain is another host", "provimux-test-mine", "apikey", "eu.openrouter.ai", a, ""},
		{"none: a login on OpenRouter's host", "provimux-test-mine", "oauth", "openrouter.ai", a, ""},
		{"none: an openai-compatibility key on an unknown host", "openai-compatibility", "apikey", "provimux-test.example", a, ""},
		{"none: a plugin's provider", "provimux-test-plugin", "oauth", "api.anthropic.com", a, ""},
		{"none: no kind", "claude", "", "api.anthropic.com", a, ""},
		{"none: no host", "claude", "oauth", "", a, ""},
		{"none: nothing", "", "", "", "", ""},
	} {
		if got := EntryProvider(tc.provider, tc.kind, tc.host, tc.signer); got != tc.want {
			t.Errorf("%s: EntryProvider(%q, %q, %q, %q) = %q, want %q", tc.row, tc.provider, tc.kind, tc.host, tc.signer, got, tc.want)
		}
	}
}

// The model of an entry: the id sent, exactly, but OpenRouter's routing suffix and a Devin request id, which is its
// model in Devin's catalog.
func TestEntryModelAndRef(t *testing.T) {
	for _, tc := range []struct{ provider, sent, want string }{
		{"openrouter", "provimux-test-lab/provimux-test-model:nitro", "provimux-test-lab/provimux-test-model"},
		{"openrouter", "provimux-test-lab/provimux-test-model:floor", "provimux-test-lab/provimux-test-model"},
		{"openrouter", "provimux-test-lab/provimux-test-model:free", "provimux-test-lab/provimux-test-model:free"},
		{"openrouter", "provimux-test-lab/provimux-test-model:online", "provimux-test-lab/provimux-test-model:online"},
		{"openrouter", "provimux-test-lab/provimux-test-model", "provimux-test-lab/provimux-test-model"},
		// Devin's embedded catalog: a model, its effort variant, its namespaced form; an id it does not know.
		{"devin", "swe-2-1", "swe-2-1"},
		{"devin", "swe-2-1_HIGH", "swe-2-1"},
		{"devin", "devin/swe-2-1", "swe-2-1"},
		{"devin", "provimux-test-not-in-the-catalog", ""},
		// The sent id is bare: nothing to strip any more.
		{"github-copilot", "copilot/provimux-test-model", "copilot/provimux-test-model"},
		{"github-copilot", "provimux-test-model", "provimux-test-model"},
		{"google-vertex", "google/provimux-test-model", "google/provimux-test-model"},
		{"anthropic", "devin/provimux-test-model", "devin/provimux-test-model"},
		{"anthropic", "provimux-test-model:nitro", "provimux-test-model:nitro"},
		{"openai-codex", "provimux-test-6.1", "provimux-test-6.1"},
		{"claude-subscription", "provimux-test-model-20261101", "provimux-test-model-20261101"},
		{"claude-subscription", " provimux-test-model ", ""},
		{"claude-subscription", "", ""},
	} {
		if got := EntryModel(tc.provider, tc.sent); got != tc.want {
			t.Errorf("EntryModel(%q, %q) = %q, want %q", tc.provider, tc.sent, got, tc.want)
		}
	}
	for _, tc := range []struct{ provider, model, want string }{
		{"openrouter", "provimux-test-lab/provimux-test-model", "openrouter/provimux-test-lab/provimux-test-model"},
		{"anthropic", "provimux-test-model", "anthropic/provimux-test-model"},
		{"", "provimux-test-model", ""},
		{"anthropic", "", ""},
		{"bad/provider", "provimux-test-model", ""},
	} {
		if got := EntryRef(tc.provider, tc.model); got != tc.want {
			t.Errorf("EntryRef(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

// Every host the Vertex executor builds for itself, and every host its own rule recognises, is Google's here; the
// rule is the executor's (helps.IsGoogleVertexHost), so the two cannot disagree on a hostname.
func TestGoogleVertexHostIsTheExecutorsRule(t *testing.T) {
	for _, location := range []string{"", "global", "us", "eu", "us-central1", "europe-west4", "asia-northeast1"} {
		base := helps.VertexOpenAPIHostBaseURL(location)
		host := strings.TrimPrefix(base, "https://")
		if !GoogleVertexHost(host) || EntryProvider("vertex", "apikey", host, "") != "google-vertex" {
			t.Errorf("location %q: the executor's host %s is not Google's here", location, host)
		}
	}
	for _, host := range []string{"aiplatform.googleapis.com", "us-central1.aiplatform.googleapis.com", "aiplatform.us.rep.googleapis.com",
		"provimux-test.example", "aiplatform.googleapis.com.provimux-test.example", "aiplatform.us.x.rep.googleapis.com"} {
		if got, executor := GoogleVertexHost(host), helps.IsGoogleVertexHost("https://"+host); got != executor {
			t.Errorf("%s: here %v, the executor %v", host, got, executor)
		}
	}
	// The executor's fallback for a base URL that does not parse would call this Google's; a host never does.
	for _, host := range []string{"", "provimux-test.example/aiplatform.googleapis.com", "user@aiplatform.googleapis.com", "aiplatform.googleapis.com:443"} {
		if GoogleVertexHost(host) {
			t.Errorf("%q is not a hostname and must not count", host)
		}
	}
}
