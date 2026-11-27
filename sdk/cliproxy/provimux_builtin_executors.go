package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the closed list of built-in executors (.github/provimux/design/answered-by.md, "Where an entry is named").
// An answer names an entry only when the executor that answers is of a type on the list below, and no executor
// that is not one of the gateway's own was ever put in place for its provider in this process: the manager marks the
// provider at the registration itself, before such an executor is in place (coreauth.provimuxRegistering), by what is
// registered, never by comparing anything afterwards. The gateway putting its own executors back never marks anything.

// provimuxBuiltinKind is the closed list: the executors the gateway's service makes for its own credentials, by type.
// The types are in an internal package, so no program outside this module can make one. AI Studio (a socket a browser
// opened) and a plugin's executor are not on it.
func provimuxBuiltinKind(exec coreauth.ProviderExecutor) string {
	switch exec.(type) {
	case *executor.CodexAutoExecutor:
		return "codex"
	case *executor.ClaudeExecutor:
		return "claude"
	case *executor.GeminiExecutor:
		return "gemini"
	case *executor.GeminiVertexExecutor:
		return "vertex"
	case *executor.AntigravityExecutor:
		return "antigravity"
	case *executor.KimiExecutor:
		return "kimi"
	case *executor.XAIAutoExecutor:
		return "xai"
	case *executor.CopilotExecutor:
		return "copilot"
	case *executor.DevinExecutor:
		return "devin"
	case *executor.MetaExecutor:
		return "meta"
	case *executor.OpenAICompatExecutor:
		return "openai-compatibility"
	}
	return ""
}

// provimuxOwnExecutor reports whether exec is one of the gateway's built-in executors: one on the closed list, or AI
// Studio's (the gateway's own, which names no entry). Registering any other turns its provider's entries off.
func provimuxOwnExecutor(exec coreauth.ProviderExecutor) bool {
	if _, aiStudio := exec.(*executor.AIStudioExecutor); aiStudio {
		return true
	}
	return provimuxBuiltinKind(exec) != ""
}
