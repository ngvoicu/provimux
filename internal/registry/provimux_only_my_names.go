package registry

// Provimux: models.only-my-names (design: .github/provimux/design/only-my-names.md, section 16). The switch
// never changes what accounts register. The rule is one immutable value, put in force with one atomic store
// when a config is committed (SetProvimuxNamesRule), and applied by name where names are read: the two loops
// that build every model list (provimuxHeld, called from model_registry.go), the door every handler passes
// before a request executes, the function that asks plugin routers, and alpha search (ProvimuxServes,
// ProvimuxRefuse). The status endpoint reads the same value and the same registry (ProvimuxNamesView).
//
// Locking: the rule is read with one atomic load. provimuxHeld runs under the registry's lock, which its
// caller holds, and takes no other. ProvimuxServes and ProvimuxRefuse take the registry's read lock only for a
// name that is not literally in the rule, and release it before touching the refusal counter, which has its
// own small lock. SetProvimuxNamesRule stores the rule and clears the list cache under the registry's lock, so
// no list built under the old rule survives the call and no list mixes two rules.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// OnlyMyNames is models.only-my-names. Absent, or enabled false, is off.
type OnlyMyNames struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Keep    []string `yaml:"keep,omitempty" json:"keep,omitempty"`
}

// Who set a name (the status endpoint's set_by).
const (
	ProvimuxNameSetByAlias     = "alias"
	ProvimuxNameSetByKeep      = "keep"
	ProvimuxNameSetByLoginFile = "login-file"
)

// Why a keep entry is ignored (the status endpoint's keep_ignored reason).
const (
	ProvimuxKeepIgnoredPattern = "pattern"
	ProvimuxKeepIgnoredEmpty   = "empty"
)

// provimuxRefusedLimit is how many names the refusal counter holds (decision 5 of the design).
const provimuxRefusedLimit = 256

// ProvimuxOwnName is a name the owner set in the config, as first written, and who set it.
type ProvimuxOwnName struct {
	Name  string
	SetBy []string
}

// ProvimuxKeepIgnored is a keep entry the gateway ignores.
type ProvimuxKeepIgnored struct {
	Value  string
	Reason string
}

// ProvimuxNamesRule is the rule of one config: the switch, Home mode, the config's names (alias rows and keep;
// compared trimmed and without case), the keep entries it ignores, and the SHA-256 of the config file it was
// read from. It is built, then put in force once, and never changed after (SetProvimuxNamesRule stores a copy
// carrying its generation). A login's own aliases are not here: they are read on the registered model
// (OAuthAlias), by name.
type ProvimuxNamesRule struct {
	enabled    bool
	home       bool
	source     string
	generation uint64
	names      map[string]*ProvimuxOwnName
	order      []string
	ignored    []ProvimuxKeepIgnored
}

// NewProvimuxNamesRule returns an empty rule. home: the gateway is in Home mode, where Home decides model lists
// and routing and the rule does not apply.
func NewProvimuxNamesRule(enabled, home bool) *ProvimuxNamesRule {
	return &ProvimuxNamesRule{enabled: enabled, home: home, names: make(map[string]*ProvimuxOwnName)}
}

// SetSource records the SHA-256 of the config file the rule was read from (the config API's ETag).
func (r *ProvimuxNamesRule) SetSource(hash string) {
	if r != nil {
		r.source = strings.TrimSpace(hash)
	}
}

// AddName adds a name the owner set. Empty names are ignored.
func (r *ProvimuxNamesRule) AddName(name, setBy string) {
	if r == nil {
		return
	}
	name = strings.TrimSpace(name)
	key := strings.ToLower(name)
	if key == "" {
		return
	}
	entry, ok := r.names[key]
	if !ok {
		entry = &ProvimuxOwnName{Name: name}
		r.names[key] = entry
		r.order = append(r.order, key)
	}
	for _, have := range entry.SetBy {
		if have == setBy {
			return
		}
	}
	entry.SetBy = append(entry.SetBy, setBy)
}

// AddKeep adds models.only-my-names.keep: exact names only. An empty entry, or one with "*", is ignored and
// reported (decision 4), never an error, so a typo cannot stop a start.
func (r *ProvimuxNamesRule) AddKeep(entries []string) {
	if r == nil {
		return
	}
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		switch {
		case trimmed == "":
			r.ignored = append(r.ignored, ProvimuxKeepIgnored{Value: entry, Reason: ProvimuxKeepIgnoredEmpty})
		case strings.Contains(trimmed, "*"):
			r.ignored = append(r.ignored, ProvimuxKeepIgnored{Value: entry, Reason: ProvimuxKeepIgnoredPattern})
		default:
			r.AddName(trimmed, ProvimuxNameSetByKeep)
		}
	}
}

// applies reports whether the rule decides what is served: set, the switch on, not Home mode.
func (r *ProvimuxNamesRule) applies() bool { return r != nil && r.enabled && !r.home }

// Applies reports whether the rule decides what is served: the switch on and not Home mode.
func (r *ProvimuxNamesRule) Applies() bool { return r.applies() }

// Enabled is models.only-my-names.enabled of the rule's config.
func (r *ProvimuxNamesRule) Enabled() bool { return r != nil && r.enabled }

// Home reports whether the rule's config is in Home mode.
func (r *ProvimuxNamesRule) Home() bool { return r != nil && r.home }

// Source is the SHA-256 of the config file the rule was read from; "" when not known.
func (r *ProvimuxNamesRule) Source() string {
	if r == nil {
		return ""
	}
	return r.source
}

// Generation counts the rules put in force since the start; this one's (0 for a rule never put in force).
func (r *ProvimuxNamesRule) Generation() uint64 {
	if r == nil {
		return 0
	}
	return r.generation
}

// Names are the config's names, in config order, each with who set it.
func (r *ProvimuxNamesRule) Names() []ProvimuxOwnName {
	if r == nil {
		return nil
	}
	out := make([]ProvimuxOwnName, 0, len(r.order))
	for _, key := range r.order {
		entry := r.names[key]
		out = append(out, ProvimuxOwnName{Name: entry.Name, SetBy: append([]string(nil), entry.SetBy...)})
	}
	return out
}

// KeepIgnored are the keep entries the rule ignores.
func (r *ProvimuxNamesRule) KeepIgnored() []ProvimuxKeepIgnored {
	if r == nil {
		return nil
	}
	return append([]ProvimuxKeepIgnored(nil), r.ignored...)
}

func (r *ProvimuxNamesRule) has(key string) bool {
	if r == nil || key == "" {
		return false
	}
	_, ok := r.names[key]
	return ok
}

// fact reports whether a model registered with these two facts makes its id the owner's (16.3, facts 2 and
// 3): it is an alias (of an oauth-model-alias row or of a login's own file, prefixed copies included), or a
// copy under an account's prefix of one of the config's names.
func (r *ProvimuxNamesRule) fact(oauthAlias, prefixedFrom string) bool {
	if oauthAlias != "" {
		return true
	}
	return prefixedFrom != "" && r.has(strings.ToLower(strings.TrimSpace(prefixedFrom)))
}

// provimuxFacts answers, for one id, whether some account registers it (registered) and whether some account
// registers it as an alias or as a prefixed copy of a name of rule (fact). The registry (under its lock) and
// the status's index (ProvimuxIndex) answer it from the same registrations.
type provimuxFacts interface {
	provimuxFact(rule *ProvimuxNamesRule, id string) (registered, fact bool)
}

// his is the one predicate (16.3): id is the owner's when it is one of the config's names, trimmed and
// without case, or some account registers it as an alias or as a prefixed copy of one of them. When id has
// upper-case letters and nothing is registered under it, its lower-case form is asked too, as upstream's own
// lookup does. It ignores the switch.
func (r *ProvimuxNamesRule) his(id string, facts provimuxFacts) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	lower := strings.ToLower(id)
	if r.has(lower) {
		return true
	}
	registered, fact := facts.provimuxFact(r, id)
	if fact {
		return true
	}
	if !registered && lower != id {
		_, fact = facts.provimuxFact(r, lower)
	}
	return fact
}

// provimuxFact implements provimuxFacts on the registry. The caller holds the registry's lock (read or write).
func (r *ModelRegistry) provimuxFact(rule *ProvimuxNamesRule, id string) (registered, fact bool) {
	if _, ok := r.models[id]; !ok {
		return false, false
	}
	return true, r.provimuxFactsLocked(rule).ids[id]
}

// provimuxFactIndex holds the ids some live client registers as an alias or as a prefixed copy of a name of
// rule, for one registry at one of its generations. Every change of what a client registers moves the
// registry's generation (invalidateAvailableModelsCacheLocked), and so does putting a rule in force, so an
// index of the current generation and rule describes the registry as it is. It is built in one pass over the
// registrations and never changed after.
type provimuxFactIndex struct {
	registry   *ModelRegistry
	rule       *ProvimuxNamesRule
	generation uint64
	ids        map[string]bool
}

var provimuxFactCache atomic.Pointer[provimuxFactIndex]

// provimuxFactsLocked returns the index of r's current generation under rule, building it when the cached one
// is of another generation, rule or registry. The caller holds r's lock (read or write); concurrent readers
// may each build one, and the last stored wins: they are equal.
func (r *ModelRegistry) provimuxFactsLocked(rule *ProvimuxNamesRule) *provimuxFactIndex {
	if cached := provimuxFactCache.Load(); cached != nil && cached.registry == r && cached.rule == rule && cached.generation == r.generation {
		return cached
	}
	index := &provimuxFactIndex{registry: r, rule: rule, generation: r.generation, ids: map[string]bool{}}
	for clientID, infos := range r.clientModelInfos {
		if _, live := r.clientModels[clientID]; !live {
			continue
		}
		for id, info := range infos {
			if info != nil && rule.fact(info.OAuthAlias, info.PrefixedFrom) {
				index.ids[id] = true
			}
		}
	}
	provimuxFactCache.Store(index)
	return index
}

// provimuxRule is the rule in force; nil until a config is committed (upstream's tests never set one).
var provimuxRule atomic.Pointer[ProvimuxNamesRule]

// provimuxGeneration counts the rules put in force. Written under the registry's lock.
var provimuxGeneration uint64

// provimuxHeld is the list gate (G1): whether a registered model is left out of every list. The caller holds
// the registry's lock.
func (r *ModelRegistry) provimuxHeld(info *ModelInfo) bool {
	rule := provimuxRule.Load()
	if !rule.applies() || info == nil {
		return false
	}
	return !rule.his(info.ID, r)
}

// ProvimuxApplies reports whether the rule in force decides what is served: one atomic load.
func ProvimuxApplies() bool { return provimuxRule.Load().applies() }

// ProvimuxServes reports whether name may be listed and asked for: true when the rule does not apply or name
// is the owner's (only-my-names.md 16.3). name has no thinking suffix.
func ProvimuxServes(name string) bool {
	rule := provimuxRule.Load()
	if !rule.applies() {
		return true
	}
	name = strings.TrimSpace(name)
	if rule.has(strings.ToLower(name)) {
		return true
	}
	reg := GetGlobalRegistry()
	reg.mutex.RLock()
	his := rule.his(name, reg)
	reg.mutex.RUnlock()
	return his
}

// ProvimuxRefuse reports whether a request for name is refused. offered: an account registers the name; the
// refusal was then counted and the caller answers with ProvimuxNotServedMessage(name). Otherwise the caller
// answers as for a model nobody offers.
func ProvimuxRefuse(name string) (refused, offered bool) {
	return ProvimuxRefuseSent(name, name)
}

// ProvimuxRefuseSent is ProvimuxRefuse for a request whose name was sent as sent (with a thinking suffix) and
// is base without it: refused when neither is the owner's; offered when an account registers either; counted
// under base.
func ProvimuxRefuseSent(base, sent string) (refused, offered bool) {
	rule := provimuxRule.Load()
	if !rule.applies() {
		return false, false
	}
	base, sent = strings.TrimSpace(base), strings.TrimSpace(sent)
	if rule.has(strings.ToLower(base)) || (sent != base && rule.has(strings.ToLower(sent))) {
		return false, false
	}
	reg := GetGlobalRegistry()
	reg.mutex.RLock()
	refused = !rule.his(base, reg) && (sent == base || !rule.his(sent, reg))
	if refused {
		offered = reg.provimuxRegisteredLocked(base) || (sent != base && reg.provimuxRegisteredLocked(sent))
	}
	reg.mutex.RUnlock()
	if refused && offered {
		provimuxCountRefusal(rule, base)
	}
	return refused, offered
}

// provimuxRegisteredLocked reports whether an account registers name, or its lower-case form when nothing is
// registered under name as given (upstream's lookup, internal/util/provider.go).
func (r *ModelRegistry) provimuxRegisteredLocked(name string) bool {
	if name == "" {
		return false
	}
	if _, ok := r.models[name]; ok {
		return true
	}
	lower := strings.ToLower(name)
	if lower == name {
		return false
	}
	_, ok := r.models[lower]
	return ok
}

// ProvimuxNotServedMessage is the message that replaces "unknown provider for model" for a refused name an
// account registers (section 5).
func ProvimuxNotServedMessage(name string) string {
	return fmt.Sprintf("model %s is not served: this Provimux serves only the names its owner set (models.only-my-names). Keep it on the console's Models page, or ask for a served name (GET /v1/models).", strings.TrimSpace(name))
}

// SetProvimuxNamesRule puts rule in force: under the registry's lock, one atomic store and the list cache
// cleared. Counting refusals starts again when the rule starts applying and is dropped when it stops. With
// the rule applying it warns, at every call (every reload), for each keep entry it ignores and when the config
// sets no name; with it off it logs nothing.
func SetProvimuxNamesRule(rule *ProvimuxNamesRule) {
	reg := GetGlobalRegistry()
	reg.mutex.Lock()
	provimuxGeneration++
	var stored *ProvimuxNamesRule
	if rule != nil {
		copyRule := *rule
		copyRule.generation = provimuxGeneration
		stored = &copyRule
	}
	wasOn := provimuxRule.Load().applies()
	provimuxRule.Store(stored)
	reg.invalidateAvailableModelsCacheLocked()
	provimuxRefusals.mu.Lock()
	switch {
	case stored.applies() && !wasOn:
		provimuxRefusals.names = make(map[string]*provimuxRefusedEntry)
		provimuxRefusals.since = provimuxRefusals.now()
		provimuxRefusals.truncated = false
	case !stored.applies():
		provimuxRefusals.names = nil
		provimuxRefusals.truncated = false
	}
	provimuxRefusals.mu.Unlock()
	reg.mutex.Unlock()
	if !stored.applies() {
		return
	}
	for _, ignored := range stored.ignored {
		log.Warnf("only-my-names: keep entry %q is ignored (%s): keep takes exact names", ignored.Value, ignored.Reason)
	}
	if len(stored.names) == 0 {
		log.Warn("only-my-names is on and no name is set in the config: only aliases in a login's own file are served")
	}
}

type provimuxRefusedEntry struct {
	name     string
	requests int
	lastAt   time.Time
}

// provimuxRefusals is the refusal counter of section 5: at most 256 names, since the rule started applying.
var provimuxRefusals = struct {
	mu        sync.Mutex
	names     map[string]*provimuxRefusedEntry // nil while the rule does not apply
	since     time.Time
	truncated bool
	now       func() time.Time
}{now: time.Now}

func provimuxCountRefusal(rule *ProvimuxNamesRule, name string) {
	provimuxRefusals.mu.Lock()
	defer provimuxRefusals.mu.Unlock()
	if provimuxRefusals.names == nil || provimuxRule.Load() != rule {
		return // the rule changed meanwhile: the refusal was the previous rule's
	}
	entry, ok := provimuxRefusals.names[name]
	switch {
	case ok:
		entry.requests++
		entry.lastAt = provimuxRefusals.now()
	case len(provimuxRefusals.names) >= provimuxRefusedLimit:
		provimuxRefusals.truncated = true
	default:
		provimuxRefusals.names[name] = &provimuxRefusedEntry{name: name, requests: 1, lastAt: provimuxRefusals.now()}
	}
}

// ProvimuxRefusedName counts the refusals of one name.
type ProvimuxRefusedName struct {
	Name     string
	Requests int
	LastAt   time.Time
}

// ProvimuxRefused is what was refused since counting began.
type ProvimuxRefused struct {
	Since     time.Time
	Names     []ProvimuxRefusedName
	Truncated bool
}

// ProvimuxRefusedNow is the refusal counter, most recent first; nil while the rule in force does not apply.
func ProvimuxRefusedNow() *ProvimuxRefused {
	provimuxRefusals.mu.Lock()
	defer provimuxRefusals.mu.Unlock()
	if provimuxRefusals.names == nil {
		return nil
	}
	refused := &ProvimuxRefused{Since: provimuxRefusals.since, Truncated: provimuxRefusals.truncated, Names: make([]ProvimuxRefusedName, 0, len(provimuxRefusals.names))}
	for _, entry := range provimuxRefusals.names {
		refused.Names = append(refused.Names, ProvimuxRefusedName{Name: entry.name, Requests: entry.requests, LastAt: entry.lastAt})
	}
	sort.Slice(refused.Names, func(i, j int) bool {
		if !refused.Names[i].LastAt.Equal(refused.Names[j].LastAt) {
			return refused.Names[i].LastAt.After(refused.Names[j].LastAt)
		}
		return refused.Names[i].Name < refused.Names[j].Name
	})
	return refused
}

// ProvimuxRegistered is one model one client registers, with the facts the rule reads.
type ProvimuxRegistered struct{ ClientID, ID, OAuthAlias, PrefixedFrom string }

// ProvimuxNamesView is the rule in force and, from one pass under one read lock and without cloning model
// metadata, every model every client registers (accounts and plugin executors alike), clients sorted, each
// client's models in its order. The status endpoint shows the enabled accounts and the plugin executors among
// them, and decides what is the owner's with His over all of them, as the gate does over the registry.
func ProvimuxNamesView() (rule *ProvimuxNamesRule, registered []ProvimuxRegistered) {
	reg := GetGlobalRegistry()
	reg.mutex.RLock()
	defer reg.mutex.RUnlock()
	rule = provimuxRule.Load()
	clients := make([]string, 0, len(reg.clientModels))
	total := 0
	for clientID, ids := range reg.clientModels {
		clients = append(clients, clientID)
		total += len(ids)
	}
	sort.Strings(clients)
	registered = make([]ProvimuxRegistered, 0, total)
	for _, clientID := range clients {
		infos := reg.clientModelInfos[clientID]
		seen := make(map[string]struct{}, len(reg.clientModels[clientID]))
		for _, id := range reg.clientModels[clientID] {
			if _, dup := seen[id]; dup || id == "" {
				continue
			}
			seen[id] = struct{}{}
			entry := ProvimuxRegistered{ClientID: clientID, ID: id}
			if info := infos[id]; info != nil {
				entry.OAuthAlias, entry.PrefixedFrom = info.OAuthAlias, info.PrefixedFrom
			}
			registered = append(registered, entry)
		}
	}
	return rule, registered
}

// ProvimuxIndex answers the rule's facts from a view's registrations, as the registry answers them for the
// gate.
type ProvimuxIndex struct {
	byID map[string][]ProvimuxRegistered
}

// NewProvimuxIndex indexes a view's registrations by id.
func NewProvimuxIndex(registered []ProvimuxRegistered) *ProvimuxIndex {
	index := &ProvimuxIndex{byID: make(map[string][]ProvimuxRegistered, len(registered))}
	for _, entry := range registered {
		index.byID[entry.ID] = append(index.byID[entry.ID], entry)
	}
	return index
}

func (x *ProvimuxIndex) provimuxFact(rule *ProvimuxNamesRule, id string) (registered, fact bool) {
	entries, ok := x.byID[id]
	if !ok {
		return false, false
	}
	for _, entry := range entries {
		if rule.fact(entry.OAuthAlias, entry.PrefixedFrom) {
			return true, true
		}
	}
	return true, false
}

// His reports whether id is the owner's under rule, read from index: the gate's predicate, whatever the switch
// says (with the switch off it is the preview).
func (r *ProvimuxNamesRule) His(id string, index *ProvimuxIndex) bool {
	return r.his(id, index)
}

// ResetProvimuxNames drops the rule and the refusal counts. For tests.
func ResetProvimuxNames() {
	reg := GetGlobalRegistry()
	reg.mutex.Lock()
	provimuxRule.Store(nil)
	provimuxFactCache.Store(nil)
	provimuxGeneration = 0
	reg.invalidateAvailableModelsCacheLocked()
	reg.mutex.Unlock()
	provimuxRefusals.mu.Lock()
	provimuxRefusals.names = nil
	provimuxRefusals.truncated = false
	provimuxRefusals.since = time.Time{}
	provimuxRefusals.mu.Unlock()
}

// SetProvimuxNamesClock replaces the clock of the refusal counter and returns a function that puts the
// previous one back. For tests.
func SetProvimuxNamesClock(now func() time.Time) func() {
	provimuxRefusals.mu.Lock()
	previous := provimuxRefusals.now
	provimuxRefusals.now = now
	provimuxRefusals.mu.Unlock()
	return func() {
		provimuxRefusals.mu.Lock()
		provimuxRefusals.now = previous
		provimuxRefusals.mu.Unlock()
	}
}
