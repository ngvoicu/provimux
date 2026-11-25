// Package onlymynames is the rule of models.only-my-names (design: .github/provimux/design/only-my-names.md,
// sections 16 to 18): which model names the gateway lists and serves while the switch is on.
//
// The rule is one immutable value built from a committed config alone: the names the config sets (the alias of
// a key's models row, the alias of an oauth-model-alias row, the entries of keep) and the model prefixes the
// config declares on its key and provider entries. Whether a name is served is a function of that value and the
// name, nothing else: this package reads no registration and cannot (it does not import the registry; a test
// checks it). The registry, the request handlers and the status call it with a value they loaded once.
package onlymynames

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Who set a name (the status endpoint's set_by).
const (
	SetByAlias = "alias"
	SetByKeep  = "keep"
)

// Why a keep entry is ignored (the status endpoint's keep_ignored reason).
const (
	KeepIgnoredPattern  = "pattern"
	KeepIgnoredEmpty    = "empty"
	KeepIgnoredReserved = "reserved"
)

// Auto is the reserved name a client sends for "the first available model": never a name the owner sets.
const Auto = "auto"

// refusedLimit is how many names the refusal counter holds (decision 5 of the design).
const refusedLimit = 256

// OwnName is a name the owner set in the config, as written (trimmed), and who set it.
type OwnName struct {
	Name  string
	SetBy []string
}

// KeepIgnored is a keep entry the gateway ignores.
type KeepIgnored struct {
	Value  string
	Reason string
}

// Rule is the rule of one config: the switch, Home mode, the config's names (trimmed, compared exactly), the
// model prefixes its entries declare, the keep entries it ignores, and the SHA-256 of the config file. It is
// built, put in force once (Put stores a copy carrying its generation), and never changed after.
type Rule struct {
	enabled    bool
	home       bool
	source     string
	generation uint64
	names      map[string]*OwnName
	order      []string
	prefixes   []string
	ignored    []KeepIgnored
}

// NewRule returns an empty rule. home: the gateway is in Home mode, where the rule does not apply.
func NewRule(enabled, home bool) *Rule {
	return &Rule{enabled: enabled, home: home, names: make(map[string]*OwnName)}
}

// SetSource records the SHA-256 of the config file the rule was read from (the config API's ETag).
func (r *Rule) SetSource(hash string) {
	if r != nil {
		r.source = strings.TrimSpace(hash)
	}
}

// AddName adds a name the owner set, trimmed. Empty names, and the reserved "auto", are ignored.
func (r *Rule) AddName(name, setBy string) {
	if r == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" || name == Auto {
		return
	}
	entry, ok := r.names[name]
	if !ok {
		entry = &OwnName{Name: name}
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

// AddKeep adds models.only-my-names.keep: exact names only. An empty entry, one with "*", or the reserved "auto"
// is ignored and reported (decision 4), never an error, so a typo cannot stop a start.
func (r *Rule) AddKeep(entries []string) {
	if r == nil {
		return
	}
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		switch {
		case trimmed == "":
			r.ignored = append(r.ignored, KeepIgnored{Value: entry, Reason: KeepIgnoredEmpty})
		case strings.Contains(trimmed, "*"):
			r.ignored = append(r.ignored, KeepIgnored{Value: entry, Reason: KeepIgnoredPattern})
		case trimmed == Auto:
			r.ignored = append(r.ignored, KeepIgnored{Value: entry, Reason: KeepIgnoredReserved})
		default:
			r.AddName(trimmed, SetByKeep)
		}
	}
}

// AddPrefix adds a model prefix the config declares on an account (the prefix field of a key or provider entry),
// trimmed as upstream trims it when it builds the account. An empty prefix is ignored.
func (r *Rule) AddPrefix(prefix string) {
	if r == nil {
		return
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return
	}
	at := sort.SearchStrings(r.prefixes, prefix)
	if at < len(r.prefixes) && r.prefixes[at] == prefix {
		return
	}
	r.prefixes = append(r.prefixes, "")
	copy(r.prefixes[at+1:], r.prefixes[at:])
	r.prefixes[at] = prefix
}

// Applies reports whether the rule decides what is served: set, the switch on, not Home mode.
func (r *Rule) Applies() bool { return r != nil && r.enabled && !r.home }

// Enabled is models.only-my-names.enabled of the rule's config.
func (r *Rule) Enabled() bool { return r != nil && r.enabled }

// Home reports whether the rule's config is in Home mode.
func (r *Rule) Home() bool { return r != nil && r.home }

// Source is the SHA-256 of the config file the rule was read from; "" when not known.
func (r *Rule) Source() string {
	if r == nil {
		return ""
	}
	return r.source
}

// Generation counts the rules put in force since the start; this one's (0 for a rule never put in force).
func (r *Rule) Generation() uint64 {
	if r == nil {
		return 0
	}
	return r.generation
}

// Names are the config's names, in config order, each with who set it.
func (r *Rule) Names() []OwnName {
	if r == nil {
		return nil
	}
	out := make([]OwnName, 0, len(r.order))
	for _, key := range r.order {
		entry := r.names[key]
		out = append(out, OwnName{Name: entry.Name, SetBy: append([]string(nil), entry.SetBy...)})
	}
	return out
}

// Prefixes are the model prefixes the config declares, sorted.
func (r *Rule) Prefixes() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.prefixes...)
}

// KeepIgnored are the keep entries the rule ignores.
func (r *Rule) KeepIgnored() []KeepIgnored {
	if r == nil {
		return nil
	}
	return append([]KeepIgnored(nil), r.ignored...)
}

// Has reports whether the config names name exactly (trimmed).
func (r *Rule) Has(name string) bool {
	if r == nil {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	_, ok := r.names[name]
	return ok
}

// CopyOf reports whether id is a copy of one of the config's names under a prefix the config declares
// ("crew/mine" for the name "mine" and the prefix "crew"), and which name.
func (r *Rule) CopyOf(id string) (string, bool) {
	if r == nil {
		return "", false
	}
	id = strings.TrimSpace(id)
	for _, prefix := range r.prefixes {
		if len(id) > len(prefix)+1 && id[len(prefix)] == '/' && strings.HasPrefix(id, prefix) {
			if name := id[len(prefix)+1:]; r.Has(name) {
				return strings.TrimSpace(name), true
			}
		}
	}
	return "", false
}

// His reports whether id is the owner's: one of the config's names, exactly, or a copy of one under a prefix
// the config declares. "auto" never is.
func (r *Rule) His(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || id == Auto {
		return false
	}
	if r.Has(id) {
		return true
	}
	_, ok := r.CopyOf(id)
	return ok
}

// Serves is the one predicate of the lists, the door, the routers, alpha search, model-routes and the status: a
// name is served when it is the owner's, or when its name without a thinking suffix is (a request for
// "opus(high)" runs "opus"). It ignores the switch: callers ask Applies first, or use Refuses.
func (r *Rule) Serves(name string) bool {
	name = strings.TrimSpace(name)
	if r.His(name) {
		return true
	}
	base := Base(name)
	return base != name && r.His(base)
}

// Refuses reports whether a request for name is refused under the rule: the rule applies and does not serve it.
func (r *Rule) Refuses(name string) bool { return r.Applies() && !r.Serves(name) }

// Base is name without a thinking suffix, as thinking.ParseSuffix cuts it: "opus(high)" is "opus".
func Base(name string) string {
	name = strings.TrimSpace(name)
	open := strings.LastIndex(name, "(")
	if open == -1 || !strings.HasSuffix(name, ")") {
		return name
	}
	return strings.TrimSpace(name[:open])
}

// NotServedMessage is the message of a refused request (section 5), naming the name without its suffix.
func NotServedMessage(name string) string {
	return fmt.Sprintf("model %s is not served: this Provimux serves only the names its owner set (models.only-my-names). Keep it on the console's Models page, or ask for a served name (GET /v1/models).", Base(name))
}

// --- The rule in force ------------------------------------------------------------------------------------

var (
	current    atomic.Pointer[Rule]
	putMu      sync.Mutex
	generation uint64
)

// Current is the rule in force (nil before any config is committed): one atomic load. A request, a list or an
// answer loads it once and decides everything under that value.
func Current() *Rule { return current.Load() }

// Put puts a copy of rule in force with the next generation, and starts or drops the refusal counter. It
// returns the stored value and whether the rule in force before applied. The registry calls it under its write
// lock, with the list cache cleared in the same section.
func Put(rule *Rule) (stored *Rule, wasOn bool) {
	putMu.Lock()
	defer putMu.Unlock()
	generation++
	if rule != nil {
		copyRule := *rule
		copyRule.generation = generation
		stored = &copyRule
	}
	wasOn = current.Load().Applies()
	current.Store(stored)
	refusals.mu.Lock()
	switch {
	case stored.Applies() && !wasOn:
		refusals.names = make(map[string]*refusedEntry)
		refusals.since = refusals.now()
		refusals.truncated = false
	case !stored.Applies():
		refusals.names = nil
		refusals.truncated = false
	}
	refusals.mu.Unlock()
	return stored, wasOn
}

// Reset drops the rule in force, the generation and the refusal counts. For tests.
func Reset() {
	putMu.Lock()
	defer putMu.Unlock()
	current.Store(nil)
	generation = 0
	refusals.mu.Lock()
	refusals.names = nil
	refusals.truncated = false
	refusals.since = time.Time{}
	refusals.mu.Unlock()
}

// --- The refusal counter ----------------------------------------------------------------------------------

type refusedEntry struct {
	name     string
	requests int
	lastAt   time.Time
}

// refusals counts refused requests per name (at most 256 names) since the rule started applying.
var refusals = struct {
	mu        sync.Mutex
	names     map[string]*refusedEntry // nil while the rule does not apply
	since     time.Time
	truncated bool
	now       func() time.Time
}{now: time.Now}

// Count counts a request refused under rule, under the name without its thinking suffix. A refusal made under a
// rule that is no longer in force is not counted.
func Count(rule *Rule, name string) {
	name = Base(name)
	refusals.mu.Lock()
	defer refusals.mu.Unlock()
	if refusals.names == nil || current.Load() != rule {
		return
	}
	entry, ok := refusals.names[name]
	switch {
	case ok:
		entry.requests++
		entry.lastAt = refusals.now()
	case len(refusals.names) >= refusedLimit:
		refusals.truncated = true
	default:
		refusals.names[name] = &refusedEntry{name: name, requests: 1, lastAt: refusals.now()}
	}
}

// RefusedName counts the refusals of one name.
type RefusedName struct {
	Name     string
	Requests int
	LastAt   time.Time
}

// Refused is what was refused since counting began.
type Refused struct {
	Since     time.Time
	Names     []RefusedName
	Truncated bool
}

// RefusedNow is the refusal counter, most recent first; nil while the rule in force does not apply.
func RefusedNow() *Refused {
	refusals.mu.Lock()
	defer refusals.mu.Unlock()
	if refusals.names == nil {
		return nil
	}
	refused := &Refused{Since: refusals.since, Truncated: refusals.truncated, Names: make([]RefusedName, 0, len(refusals.names))}
	for _, entry := range refusals.names {
		refused.Names = append(refused.Names, RefusedName{Name: entry.name, Requests: entry.requests, LastAt: entry.lastAt})
	}
	sort.Slice(refused.Names, func(i, j int) bool {
		if !refused.Names[i].LastAt.Equal(refused.Names[j].LastAt) {
			return refused.Names[i].LastAt.After(refused.Names[j].LastAt)
		}
		return refused.Names[i].Name < refused.Names[j].Name
	})
	return refused
}

// SetClock replaces the counter's clock and returns a function that puts the previous one back. For tests.
func SetClock(now func() time.Time) func() {
	refusals.mu.Lock()
	previous := refusals.now
	refusals.now = now
	refusals.mu.Unlock()
	return func() {
		refusals.mu.Lock()
		refusals.now = previous
		refusals.mu.Unlock()
	}
}
