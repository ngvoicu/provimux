package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

// Provimux: the seam of session-route (.github/provimux/design/session-route.md 7.1). Every use of a name upstream
// does not export is here and nowhere else, so a rename upstream stops the build of this one file and a change of
// meaning is caught by TestProvimuxSessionRouteSeam. Nothing here selects, sends or changes an account; the only
// write is provimuxSeamRelease, which removes one binding group under the cache's lock, as upstream's own
// CompareAndDelete would.

// Affinity modes as the contract names them (session-route/openapi.yaml, SessionRoute.affinity).
const (
	provimuxAffinityOn           = "on"
	provimuxAffinityOff          = "off"
	provimuxAffinityHome         = "home"
	provimuxAffinityNotSupported = "not-supported"
)

// provimuxSeamAffinity returns the session-affinity selector that runs on m, and the mode as the contract names it.
// Home mode first (Home selects), then a plugin scheduler (upstream's own lookup answers "unsupported" there), then
// whether the selector in use binds at all.
func provimuxSeamAffinity(m *Manager) (*SessionAffinitySelector, string) {
	if m == nil {
		return nil, provimuxAffinityOff
	}
	if m.HomeEnabled() {
		return nil, provimuxAffinityHome
	}
	if m.hasPluginScheduler() {
		return nil, provimuxAffinityNotSupported
	}
	affinity, ok := m.Selector().(*SessionAffinitySelector)
	if !ok || affinity == nil || affinity.cache == nil {
		return nil, provimuxAffinityOff
	}
	return affinity, provimuxAffinityOn
}

// ProvimuxSessionAffinityMode is session affinity as it runs on m, as the contract names it: "on", "off", "home" or
// "not-supported".
func ProvimuxSessionAffinityMode(m *Manager) string {
	_, mode := provimuxSeamAffinity(m)
	return mode
}

// ProvimuxResultObservers is the number of observers subscribed to m's results besides the constructor's hook
// (Manager.AddHook). For tests: with nothing subscribed, a result costs what it costs without Provimux.
func ProvimuxResultObservers(m *Manager) int {
	if m == nil || m.hook == nil {
		return 0
	}
	observers := m.hook.observers.Load()
	if observers == nil {
		return 0
	}
	return len(*observers)
}

// provimuxKeyDelimiter is the separator upstream joins a binding key's parts with (selector.go, cacheKey).
const provimuxKeyDelimiter = "::"

// provimuxSeamKey is a binding's key in upstream's cache, and its parts as received, kept apart.
type provimuxSeamKey struct {
	Key        string // provider::session::model, exactly as Pick made it: only ever handed to upstream's cache
	Provider   string // the provider part: a scheduling provider, or "mixed"
	Session    string // the bounded session identity
	ModelKey   string // canonicalModelKey of the route model
	RouteModel string // the route model as Pick received it
	Parent     string // the parent conversation's identity, for a sub-agent (never a fork's source or an alias)
	Socket     bool   // the turn came on a connection pinned to its login (a Codex WebSocket's execution session)
	Derived    bool   // the keys were not in the result's metadata and were derived as OnResult derives them
}

// provimuxSeamResultKey returns the cache key the result's binding was made under. First from what Pick wrote into
// the request's metadata (the provider, the model and the canonical session id); where a path lost them, derived as
// SessionAffinitySelector.OnResult derives them. ok is false for a conversation bound by its content only (the prefix
// matcher), for one with no session at all, and for one any of whose three parts holds upstream's delimiter: then
// another triple can join to the same key, upstream holds one binding for both, and the book cannot tell whose it is
// (design section 3), so it notes neither. The caller still requires the cache to hold the key (I1).
func provimuxSeamResultKey(res Result) (provimuxSeamKey, bool) {
	metadata := res.Options.Metadata
	if metadata != nil {
		if _, isLCP := metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey]; isLCP {
			if explicitID, _ := extractExplicitSessionIDs(res.Options.Headers, res.Options.OriginalRequest, nil); explicitID == "" {
				return provimuxSeamKey{}, false
			}
		}
	}
	provider := sessionMetadataString(metadata, cliproxyexecutor.SessionAffinityProviderMetadataKey)
	routeModel := sessionMetadataString(metadata, cliproxyexecutor.SessionAffinityModelMetadataKey)
	session := sessionMetadataString(metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey)
	isFork, _ := metadata[cliproxyexecutor.IsForkMetadataKey].(bool)
	derived := false
	if provider == "" || routeModel == "" || session == "" {
		derived = true
		if provider == "" {
			provider = res.Provider
		}
		if routeModel == "" {
			routeModel = res.Model
		}
		// The extraction writes fork and parent flags into the metadata it is given: an observer gives it a copy.
		scratch := make(map[string]any, len(metadata))
		for k, v := range metadata {
			scratch[k] = v
		}
		explicitID, _ := extractExplicitSessionIDs(res.Options.Headers, res.Options.OriginalRequest, scratch)
		if explicitID == "" {
			explicitID, _ = extractSessionIDs(res.Options.Headers, res.Options.OriginalRequest, scratch)
		}
		if explicitID != "" {
			session = cliproxysession.BoundSessionIdentity(explicitID)
		}
		if fork, _ := scratch[cliproxyexecutor.IsForkMetadataKey].(bool); fork {
			isFork = true
		}
	}
	provider = strings.TrimSpace(provider)
	modelKey := canonicalModelKey(routeModel)
	if provider == "" || session == "" || modelKey == "" {
		return provimuxSeamKey{}, false
	}
	if provimuxSeamAmbiguous(provider, session, modelKey) {
		return provimuxSeamKey{}, false
	}
	// The metadata's parent is also where Pick puts an alias of the same conversation (a prompt_cache_key's
	// conversation id) and a fork's source: it is a parent only where upstream's own test says the session is a
	// sub-agent's, and never for a fork.
	parent := sessionMetadataString(metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
	if parent != "" && (isFork || !isSubagentSession(session, parent)) {
		parent = ""
	}
	socket := sessionMetadataString(metadata, cliproxyexecutor.ExecutionSessionMetadataKey) != "" ||
		sessionMetadataString(metadata, cliproxyexecutor.PinnedAuthMetadataKey) != ""
	return provimuxSeamKey{
		Key:        provider + provimuxKeyDelimiter + session + provimuxKeyDelimiter + modelKey,
		Provider:   provider,
		Session:    session,
		ModelKey:   modelKey,
		RouteModel: routeModel,
		Parent:     parent,
		Socket:     socket,
		Derived:    derived,
	}, true
}

// provimuxSeamAmbiguous reports whether a triple's joined key could also be another triple's: any part holding the
// delimiter makes the join ambiguous.
func provimuxSeamAmbiguous(provider, session, modelKey string) bool {
	return strings.Contains(provider, provimuxKeyDelimiter) || strings.Contains(session, provimuxKeyDelimiter) ||
		strings.Contains(modelKey, provimuxKeyDelimiter)
}

// provimuxSeamFailureRemoves reports whether upstream's own OnResult, which runs right after the result hooks, removes
// the binding of res's key from the cache when it is bound to res's account: the same tests in the same order
// (SessionAffinitySelector.OnResult). The book ends that binding's life there, so a token of it never names the
// binding made next, even on the same account.
func provimuxSeamFailureRemoves(affinity *SessionAffinitySelector, res Result) bool {
	if affinity == nil || res.Success {
		return false
	}
	if res.Error != nil && shouldSkipCredentialCooldown(res.Error) {
		return false
	}
	if affinity.rebindOnNever || (affinity.rebindOnQuotaOnly && !shouldRebindSessionAffinityOnError(res.Error)) {
		return false
	}
	return true
}

// provimuxSeamNamedSession returns the bounded identity a turn with these headers and this body is bound under, read
// by upstream's own extraction (the function Pick calls first), and the client type it saw. "" when the request names
// no conversation: one bound by its content only cannot be named.
func provimuxSeamNamedSession(headers http.Header, body []byte) (session, clientType string) {
	info, ok := cliproxysession.ExtractSessionInfo(headers, body, nil)
	if !ok || info.ClientType == "lcp" {
		return "", ""
	}
	explicitID, _ := extractExplicitSessionIDs(headers, body, nil)
	if explicitID == "" {
		return "", ""
	}
	return cliproxysession.BoundSessionIdentity(explicitID), info.ClientType
}

// provimuxSeamModelKey is upstream's model part of a binding key: the name without its thinking suffix.
func provimuxSeamModelKey(model string) string {
	return canonicalModelKey(model)
}

// provimuxSeamBinding is what upstream's cache holds under a key now.
type provimuxSeamBinding struct {
	AuthID    string
	ExpiresAt time.Time
	TTL       time.Duration
	Session   string        // the session of the selection that made or last chose the binding ("" when unknown)
	Cache     *SessionCache // the cache instance: a new one when the routing settings change
	// Returned is the number of the rule's last return of a binding under this key (design section 15), 0 when none
	// is known: a binding made after a return is another life, even on the same account.
	Returned uint64
}

func provimuxSeamBindingOf(affinity *SessionAffinitySelector, key string, entry sessionEntry) provimuxSeamBinding {
	cache := affinity.cache
	return provimuxSeamBinding{AuthID: entry.authID, ExpiresAt: entry.expiresAt, TTL: cache.ttl,
		Session: entry.binding.sessionID, Cache: cache, Returned: affinity.provimux.returnedOfLocked(key)}
}

// provimuxSeamLookup reads the binding under key without touching it (no refresh, no eviction order), under the
// cache's read lock. An expired entry is no binding, as for Get.
func provimuxSeamLookup(affinity *SessionAffinitySelector, key string) (provimuxSeamBinding, bool) {
	if affinity == nil || affinity.cache == nil || key == "" {
		return provimuxSeamBinding{}, false
	}
	cache := affinity.cache
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	entry, ok := cache.entries[key]
	if !ok || entry.authID == "" || !time.Now().Before(entry.expiresAt) {
		return provimuxSeamBinding{}, false
	}
	return provimuxSeamBindingOf(affinity, key, entry), true
}

// provimuxSeamTTL is the session cache's TTL: how long a binding lives after its last use.
func provimuxSeamTTL(affinity *SessionAffinitySelector) time.Duration {
	if affinity == nil || affinity.cache == nil {
		return 0
	}
	return affinity.cache.ttl
}

// provimuxSeamBeforeRemove runs in a release between the show of the binding it acts on (its look-up) and the
// locked step that checks and removes it. Only tests set it, to prove that a binding changed in between is left.
var provimuxSeamBeforeRemove func()

// provimuxSeamRelease removes the binding under key, with every alias of its group, only while still holds for the
// binding as it is under the cache's write lock. Unlike Invalidate, no alias of the group survives to bring the
// binding back. It reports whether it removed it.
func provimuxSeamRelease(affinity *SessionAffinitySelector, key string, still func(provimuxSeamBinding) bool) bool {
	if affinity == nil || affinity.cache == nil || key == "" || still == nil {
		return false
	}
	cache := affinity.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.ensureInitializedLocked()
	entry, ok := cache.entries[key]
	if !ok || entry.authID == "" || !time.Now().Before(entry.expiresAt) || !still(provimuxSeamBindingOf(affinity, key, entry)) {
		return false
	}
	cache.removeAliasGroupLocked(entry)
	return true
}

// provimuxSeamRoutes returns the accounts that serve routeModel for a binding under provider (a scheduling provider,
// or "mixed" for all), as the conductor gathers its candidates (enabled, the provider's, supporting the route model;
// with the weighted strategy only positive weights), and of them the ones available now, judged by the conductor's
// own availability across priorities at now. Every returned Auth is a clone.
func provimuxSeamRoutes(m *Manager, provider, routeModel string, now time.Time) (serving, available []*Auth) {
	if m == nil {
		return nil, nil
	}
	selector := m.Selector()
	weighted := UnwrapWeightedRoundRobinSelector(selector) != nil
	if affinity, ok := selector.(*SessionAffinitySelector); ok && affinity != nil {
		_, weighted = affinity.fallback.(*WeightedRoundRobinSelector)
	}
	targetKey := canonicalSchedulingProvider(provider)
	registryRef := registry.GetGlobalRegistry()
	m.mu.RLock()
	defer m.mu.RUnlock()
	candidates := make([]*Auth, 0, len(m.auths))
	for _, candidate := range m.auths {
		if isAuthDisabled(candidate) {
			continue
		}
		candidateKey := canonicalSchedulingProvider(executorKeyFromAuth(candidate))
		if targetKey != "mixed" && candidateKey != targetKey {
			continue
		}
		if targetKey == "mixed" {
			if _, okExecutor := m.executorLocked(candidateKey); !okExecutor {
				continue
			}
		}
		if strings.TrimSpace(routeModel) != "" && !m.authSupportsRouteModel(registryRef, candidate, routeModel) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if weighted {
		candidates = positiveWeightAuths(candidates)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	free, errAvailable := m.availableAuthsForRouteModelForRequest(nil, candidates, provider, routeModel, now, true)
	if errAvailable != nil {
		free = nil
	}
	return cloneAuthSlice(candidates), cloneAuthSlice(free)
}

// provimuxSeamHighest is upstream's highestPriorityAuths: the accounts of the highest priority among auths.
func provimuxSeamHighest(auths []*Auth) []*Auth {
	return highestPriorityAuths(auths)
}
