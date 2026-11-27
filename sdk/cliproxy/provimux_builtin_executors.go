package cliproxy

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the closed list of built-in executors (.github/provimux/design/answered-by.md, the lead's amendment of
// 2026-11-27 (2), rule A.1). An answer names an entry only when the executor that answers is one the gateway's own
// service registered (Service.registerExecutor, for the credentials of its own configuration and auth folder), of a type
// on the list below, not wrapped by an embedding's ExecutorWrapper, and no registration for that provider put another
// kind of executor in place while the call ran (the amendment of 2026-11-27 (2), part 4). The record is kept here, per manager, in memory; only
// Service.registerExecutor writes it. An executor another program registers (Manager.RegisterExecutor) is never on it,
// and from the moment one is registered for a provider until the service registers its own again with nothing in
// between, the provider's answers name no entry.

// provimuxExecutorRecord is the service's last registration for a provider.
type provimuxExecutorRecord struct {
	exec     coreauth.ProviderExecutor // what the service registered
	revision uint64                    // the manager's revision of that registration
	kind     string                    // its kind on the closed list; "" = not on it
	// changed is the event number of the last registration for the provider that put another kind of executor in
	// place than the service's built-in one before it: another program's (seen as a gap in the revisions), an
	// unregistration, or the service's own of another kind. 0 = none.
	changed uint64
}

var provimuxExecutors struct {
	mu   sync.Mutex
	byMg map[*coreauth.Manager]map[string]provimuxExecutorRecord
}

// provimuxExecutorEvents numbers the changes of kind recorded above, in the order they were seen. A call notes the
// number at its first attempt; its answer names an entry only if no change for its provider came after it.
var provimuxExecutorEvents atomic.Uint64

// provimuxExecutorSequence is the number a call notes at its first attempt.
func provimuxExecutorSequence() uint64 { return provimuxExecutorEvents.Load() }

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

// provimuxNoteExecutor records the service's registration of exec (owned before any ExecutorWrapper) for provider.
// Only Service.registerExecutor calls it, right after the manager returned the registration's revision.
func provimuxNoteExecutor(m *coreauth.Manager, provider string, exec, owned coreauth.ProviderExecutor, revision uint64) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if m == nil || provider == "" || exec == nil || revision == 0 {
		return
	}
	kind := ""
	if provimuxSameExecutor(exec, owned) { // not wrapped by an embedding's ExecutorWrapper
		kind = provimuxBuiltinKind(exec)
	}
	provimuxExecutors.mu.Lock()
	defer provimuxExecutors.mu.Unlock()
	if provimuxExecutors.byMg == nil {
		provimuxExecutors.byMg = map[*coreauth.Manager]map[string]provimuxExecutorRecord{}
	}
	records := provimuxExecutors.byMg[m]
	if records == nil {
		records = map[string]provimuxExecutorRecord{}
		provimuxExecutors.byMg[m] = records
	}
	// The service putting its own built-in executor back, with nothing between, is no change: a reload, a login
	// added or refreshed, a model-list refresh all do it. Another registration for the provider between the service's
	// previous one and this one (an embedding's executor, a plugin's, an unregistration), or the service's own of
	// another kind, is: a call that started before this one may have run on what was in place.
	previous, known := records[provider]
	changed := previous.changed
	if !known || revision != previous.revision+1 || kind != previous.kind {
		if known || revision != 1 {
			changed = provimuxExecutorEvents.Add(1)
		}
	}
	records[provider] = provimuxExecutorRecord{exec: exec, revision: revision, kind: kind, changed: changed}
}

// provimuxBuiltinExecutor returns the closed-list kind of exec, the executor m publishes for an answering credential
// at revision, when the service registered exactly it at exactly that revision, and no registration for the provider
// put another kind of executor in place after the call noted since (its first attempt); "" else.
func provimuxBuiltinExecutor(m *coreauth.Manager, exec coreauth.ProviderExecutor, revision, since uint64) string {
	if m == nil || exec == nil {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(exec.Identifier()))
	provimuxExecutors.mu.Lock()
	record, ok := provimuxExecutors.byMg[m][provider]
	provimuxExecutors.mu.Unlock()
	if !ok || record.kind == "" || record.revision != revision || !provimuxSameExecutor(record.exec, exec) || record.changed > since {
		return ""
	}
	return record.kind
}

// provimuxSameExecutor reports whether a and b are the same executor, by identity: both the same pointer (every
// built-in executor is one). Values of another kind are never compared, so none can panic.
func provimuxSameExecutor(a, b coreauth.ProviderExecutor) bool {
	if a == nil || b == nil || reflect.TypeOf(a) != reflect.TypeOf(b) || reflect.TypeOf(a).Kind() != reflect.Pointer {
		return false
	}
	return a == b
}
