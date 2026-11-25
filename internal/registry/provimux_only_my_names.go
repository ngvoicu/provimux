package registry

// Provimux: models.only-my-names (design: .github/provimux/design/only-my-names.md, sections 16 and 17). The
// switch never changes what accounts register. The rule is one immutable value built from the committed config
// alone, put in force with one atomic store when the config is committed (SetProvimuxNamesRule), and applied
// by name where names are read: the two loops that build every model list (provimuxHeld, called from
// model_registry.go), the door every handler passes before a request executes, the function that asks plugin
// routers, and alpha search (ProvimuxServes, ProvimuxRefuse). The status endpoint reads the same value and the
// same registry, and judges every id with the same predicate (ProvimuxNamesView, (*ProvimuxNamesRule).Serves).
//
// Names are compared exactly, trimmed and nothing else, as upstream matches a request to a registered model.
//
// Locking: the rule is read with one atomic load. provimuxHeld runs under the registry's lock, which its
// caller holds, and takes no other. ProvimuxServes and ProvimuxRefuse take the registry's read lock only for a
// name the rule does not name literally, and release it before touching the refusal counter, which has its own
// small lock. SetProvimuxNamesRule stores the rule and clears the list cache under the registry's lock, so no
// list built under the old rule survives the call and no list mixes two rules.

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
	ProvimuxNameSetByAlias = "alias"
	ProvimuxNameSetByKeep  = "keep"
)

// Why a keep entry is ignored (the status endpoint's keep_ignored reason).
const (
	ProvimuxKeepIgnoredPattern  = "pattern"
	ProvimuxKeepIgnoredEmpty    = "empty"
	ProvimuxKeepIgnoredReserved = "reserved"
)

// provimuxAuto is the reserved name a client sends for "the first available model": never a name the owner
// sets (section 16.3).
const provimuxAuto = "auto"

// provimuxRefusedLimit is how many names the refusal counter holds (decision 5 of the design).
const provimuxRefusedLimit = 256

// ProvimuxOwnName is a name the owner set in the config, as written (trimmed), and who set it.
type ProvimuxOwnName struct {
	Name  string
	SetBy []string
}

// ProvimuxKeepIgnored is a keep entry the gateway ignores.
type ProvimuxKeepIgnored struct {
	Value  string
	Reason string
}

// ProvimuxNamesRule is the rule of one config: the switch, Home mode, the config's names (alias rows and keep,
// trimmed, compared exactly), the keep entries it ignores, and the SHA-256 of the config file it was read from.
// It is a function of the config alone. It is built, then put in force once, and never changed after
// (SetProvimuxNamesRule stores a copy carrying its generation).
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

// AddName adds a name the owner set, trimmed. Empty names, and the reserved "auto", are ignored.
func (r *ProvimuxNamesRule) AddName(name, setBy string) {
	if r == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" || name == provimuxAuto {
		return
	}
	entry, ok := r.names[name]
	if !ok {
		entry = &ProvimuxOwnName{Name: name}
		r.names[name] = entry
		r.order = append(r.order, name)
	}
	for _, have := range entry.SetBy {
		if have == setBy {
			return
		}
	}
	entry.SetBy = append(entry.SetBy, setBy)
}

// AddKeep adds models.only-my-names.keep: exact names only. An empty entry, one with "*", or the reserved
// "auto" is ignored and reported (decision 4), never an error, so a typo cannot stop a start.
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
		case trimmed == provimuxAuto:
			r.ignored = append(r.ignored, ProvimuxKeepIgnored{Value: entry, Reason: ProvimuxKeepIgnoredReserved})
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

// Has reports whether the config names name exactly (trimmed).
func (r *ProvimuxNamesRule) Has(name string) bool { return r.has(strings.TrimSpace(name)) }

func (r *ProvimuxNamesRule) has(name string) bool {
	if r == nil || name == "" {
		return false
	}
	_, ok := r.names[name]
	return ok
}

// copyOf reports whether a model registered as a copy of prefixedFrom under an account's prefix is a copy of
// one of the config's names.
func (r *ProvimuxNamesRule) copyOf(prefixedFrom string) bool {
	return prefixedFrom != "" && r.has(strings.TrimSpace(prefixedFrom))
}

// provimuxFacts answers, for one id, whether some account registers it as a prefixed copy of a name of rule.
// The registry (under its lock) and the status's index (ProvimuxIndex) answer it from the same registrations.
type provimuxFacts interface {
	provimuxCopy(rule *ProvimuxNamesRule, id string) bool
}

// his reports whether id is the owner's: one of the config's names, exactly; or a copy of one under an
// account's prefix (some account registers id with PrefixedFrom one of them). "auto" never is.
func (r *ProvimuxNamesRule) his(id string, facts provimuxFacts) bool {
	if id == "" || id == provimuxAuto {
		return false
	}
	return r.has(id) || facts.provimuxCopy(r, id)
}

// serves is the one predicate of the lists, the door, the routers, alpha search, model-routes and the status
// (16.3): a name is served when it is the owner's, or when its name without a thinking suffix is (a request
// for "opus(high)" runs "opus"). It ignores the switch.
func (r *ProvimuxNamesRule) serves(name string, facts provimuxFacts) bool {
	name = strings.TrimSpace(name)
	if r.his(name, facts) {
		return true
	}
	base := provimuxBase(name)
	return base != name && r.his(base, facts)
}

// literal is serves without the facts: true when the config names the name or its base. It needs no lock.
func (r *ProvimuxNamesRule) literal(name string) bool {
	name = strings.TrimSpace(name)
	if name == provimuxAuto {
		return false
	}
	if r.has(name) {
		return true
	}
	base := provimuxBase(name)
	return base != name && base != provimuxAuto && r.has(base)
}

// provimuxBase is name without a thinking suffix, as thinking.ParseSuffix cuts it (which this package cannot
// import): "opus(high)" is "opus".
func provimuxBase(name string) string {
	open := strings.LastIndex(name, "(")
	if open == -1 || !strings.HasSuffix(name, ")") {
		return name
	}
	return strings.TrimSpace(name[:open])
}

// provimuxCopy implements provimuxFacts on the registry. The caller holds the registry's lock (read or write).
func (r *ModelRegistry) provimuxCopy(rule *ProvimuxNamesRule, id string) bool {
	if _, ok := r.models[id]; !ok {
		return false
	}
	return r.provimuxFactsLocked(rule).ids[id]
}

// provimuxFactIndex holds the ids some live client registers as a prefixed copy of a name of rule, for one
// registry at one of its generations. Every change of what a client registers moves the registry's generation
// (invalidateAvailableModelsCacheLocked), and so does putting a rule in force, so an index of the current
// generation and rule describes the registry as it is. It is built in one pass over the registrations and
// never changed after; it is dropped when the rule stops applying.
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
			if info != nil && rule.copyOf(info.PrefixedFrom) {
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

// provimuxHeld is the list gate (G1): whether a registered model is left out of the list built for handlerType
// ("" for the lists of model infos). A model is listed when its id is served; the Gemini format names a model
// by Name, so there a Name that differs from the id must be served too, or the model is left out of that format
// (a plugin executor's provider-native name, section 17). The caller holds the registry's lock.
func (r *ModelRegistry) provimuxHeld(info *ModelInfo, handlerType string) bool {
	rule := provimuxRule.Load()
	if !rule.applies() || info == nil {
		return false
	}
	if !rule.serves(info.ID, r) {
		return true
	}
	if handlerType == "gemini" && info.Name != "" {
		if name := strings.TrimPrefix(strings.TrimSpace(info.Name), "models/"); name != strings.TrimSpace(info.ID) {
			return !rule.serves(name, r)
		}
	}
	return false
}

// ProvimuxApplies reports whether the rule in force decides what is served: one atomic load.
func ProvimuxApplies() bool { return provimuxRule.Load().applies() }

// ProvimuxRuleNow is the rule in force (nil before any config is committed), for an answer that must be built
// from one rule: decide with ProvimuxServesUnder.
func ProvimuxRuleNow() *ProvimuxNamesRule { return provimuxRule.Load() }

// ProvimuxServes reports whether name may be listed and asked for: true when the rule in force does not apply
// or name is served (only-my-names.md 16.3, the name as a client sends it, with or without a thinking suffix).
func ProvimuxServes(name string) bool { return ProvimuxServesUnder(provimuxRule.Load(), name) }

// ProvimuxServesUnder is ProvimuxServes under the given rule.
func ProvimuxServesUnder(rule *ProvimuxNamesRule, name string) bool {
	if !rule.applies() {
		return true
	}
	if rule.literal(name) {
		return true
	}
	reg := GetGlobalRegistry()
	reg.mutex.RLock()
	served := rule.serves(name, reg)
	reg.mutex.RUnlock()
	return served
}

// ProvimuxRefuse reports whether a request for name (as the client sent it, a thinking suffix included) is
// refused. offered: an account registers the name or its base; the refusal was then counted under the base and
// the caller answers with ProvimuxNotServedMessage(base). Otherwise the caller answers as for a model nobody
// offers.
func ProvimuxRefuse(name string) (refused, offered bool) {
	rule := provimuxRule.Load()
	if !rule.applies() || rule.literal(name) {
		return false, false
	}
	name = strings.TrimSpace(name)
	base := provimuxBase(name)
	reg := GetGlobalRegistry()
	reg.mutex.RLock()
	refused = !rule.serves(name, reg)
	if refused {
		_, offered = reg.models[name]
		if !offered && base != name {
			_, offered = reg.models[base]
		}
	}
	reg.mutex.RUnlock()
	if refused && offered {
		provimuxCountRefusal(rule, base)
	}
	return refused, offered
}

// ProvimuxNotServedMessage is the message that replaces "unknown provider for model" for a refused name an
// account registers (section 5).
func ProvimuxNotServedMessage(name string) string {
	return fmt.Sprintf("model %s is not served: this Provimux serves only the names its owner set (models.only-my-names). Keep it on the console's Models page, or ask for a served name (GET /v1/models).", strings.TrimSpace(name))
}

// SetProvimuxNamesRule puts rule in force: under the registry's lock, one atomic store and, when the rule in
// force applies before or after, the list cache cleared. Counting refusals starts again when the rule starts
// applying, and the counter and the index of facts are dropped when it stops. With the rule applying it warns,
// at every call (every reload), for each keep entry it ignores and when the config sets no name; with it off it
// logs nothing.
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
	if wasOn || stored.applies() {
		reg.invalidateAvailableModelsCacheLocked()
	}
	if !stored.applies() {
		provimuxFactCache.Store(nil)
	}
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
		log.Warn("only-my-names is on and no name is set in the config: nothing is served")
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
	return provimuxRefusedLocked()
}

func provimuxRefusedLocked() *ProvimuxRefused {
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

// ProvimuxRegistered is one model one client registers, with the fact the rule reads.
type ProvimuxRegistered struct{ ClientID, ID, PrefixedFrom string }

// ProvimuxNamesView is one consistent reading for the status: the rule in force, the refusal counter of that
// rule (nil unless it applies), and every model every client registers (accounts and plugin executors alike;
// clients sorted, each client's models in its order), all under one read lock of the registry and without
// cloning model metadata. A rule is put in force under the registry's write lock, so the three belong together.
func ProvimuxNamesView() (rule *ProvimuxNamesRule, registered []ProvimuxRegistered, refused *ProvimuxRefused) {
	reg := GetGlobalRegistry()
	reg.mutex.RLock()
	defer reg.mutex.RUnlock()
	rule = provimuxRule.Load()
	if rule.applies() {
		provimuxRefusals.mu.Lock()
		refused = provimuxRefusedLocked()
		provimuxRefusals.mu.Unlock()
	}
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
				entry.PrefixedFrom = info.PrefixedFrom
			}
			registered = append(registered, entry)
		}
	}
	return rule, registered, refused
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

func (x *ProvimuxIndex) provimuxCopy(rule *ProvimuxNamesRule, id string) bool {
	for _, entry := range x.byID[id] {
		if rule.copyOf(entry.PrefixedFrom) {
			return true
		}
	}
	return false
}

// Serves reports whether a request for id is served under rule, read from index: the gate's and the door's
// predicate, whatever the switch says (with the switch off it is the preview).
func (r *ProvimuxNamesRule) Serves(id string, index *ProvimuxIndex) bool {
	return r.serves(id, index)
}

// ProvimuxBase is name without a thinking suffix, as the gate cuts it.
func ProvimuxBase(name string) string { return provimuxBase(strings.TrimSpace(name)) }

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
