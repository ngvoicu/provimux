package registry

// Provimux: models.only-my-names (design: .github/provimux/design/only-my-names.md). With the switch
// on, an account's model is registered only under a name the owner set; every other name is held
// back, as an excluded model is: not listed, and refused as unknown with a message that names the
// setting. The rule runs in front of the registry, in the three places accounts hand it their models
// (ProvimuxOnlyMyNames), so every list and every request path follows without an edit of its own.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
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

// ProvimuxNamesRule is the rule as the process holds it: the switch, Home mode, the config's names
// (alias rows and keep; compared trimmed and without case) and the keep entries it ignores. A login's
// own aliases are not here: they belong to that login and are recognised on the model (OAuthAlias).
type ProvimuxNamesRule struct {
	enabled bool
	home    bool
	names   map[string]*ProvimuxOwnName
	order   []string
	ignored []ProvimuxKeepIgnored
}

// NewProvimuxNamesRule returns an empty rule. home: the gateway is in Home mode, where Home decides
// model lists and routing and the rule does not apply.
func NewProvimuxNamesRule(enabled, home bool) *ProvimuxNamesRule {
	return &ProvimuxNamesRule{enabled: enabled, home: home, names: make(map[string]*ProvimuxOwnName)}
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

// AddKeep adds models.only-my-names.keep: exact names only. An empty entry, or one with "*", is
// ignored and reported (decision 4), never an error, so a typo cannot stop a start.
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

// applies reports whether the rule holds names back now: the switch is on and not in Home mode.
func (r *ProvimuxNamesRule) applies() bool { return r != nil && r.enabled && !r.home }

func (r *ProvimuxNamesRule) has(key string) bool {
	if r == nil || key == "" {
		return false
	}
	_, ok := r.names[key]
	return ok
}

// serves reports whether the rule serves a model registered as id on an account with prefix:
// a login alias (loginAlias: OAuthAlias is set; its prefixed copies too), a name of the config, or
// "<prefix>/<name>" for a name of the config. Without a rule nothing is a name.
func (r *ProvimuxNamesRule) serves(id string, loginAlias bool, prefix string) bool {
	if loginAlias {
		return true
	}
	key := strings.ToLower(strings.TrimSpace(id))
	if r.has(key) {
		return true
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix != "" && strings.HasPrefix(key, prefix+"/") {
		return r.has(key[len(prefix)+1:])
	}
	return false
}

// provimuxOffered is one model an account handed the registry, before the rule.
type provimuxOffered struct {
	id         string
	loginAlias bool
}

// provimuxRecord is what an account offered at its last computation, and its prefix. The rule is
// applied to it again when it is read, so the preview follows the config without a re-registration.
type provimuxRecord struct {
	prefix  string
	offered []provimuxOffered
}

type provimuxRefusedEntry struct {
	name     string
	requests int
	lastAt   time.Time
}

var provimuxNames = struct {
	mu        sync.Mutex
	rule      *ProvimuxNamesRule
	records   map[string]provimuxRecord
	refused   map[string]*provimuxRefusedEntry
	since     time.Time
	truncated bool
	now       func() time.Time
}{records: make(map[string]provimuxRecord), now: time.Now}

// SetProvimuxNamesRule installs the rule of a committed config. Counting refusals starts again when
// the rule starts applying. With the switch on and no name set it warns, at every call (every reload).
func SetProvimuxNamesRule(rule *ProvimuxNamesRule) {
	provimuxNames.mu.Lock()
	wasOn := provimuxNames.rule.applies()
	provimuxNames.rule = rule
	if rule.applies() && !wasOn {
		provimuxNames.refused = make(map[string]*provimuxRefusedEntry)
		provimuxNames.since = provimuxNames.now()
		provimuxNames.truncated = false
	}
	if !rule.applies() {
		provimuxNames.refused = nil
		provimuxNames.truncated = false
	}
	provimuxNames.mu.Unlock()
	if rule.applies() && len(rule.names) == 0 {
		log.Warn("only-my-names is on and no name is set: no model is served")
	}
}

// ProvimuxOnlyMyNames returns the models of clientID that are served under the current rule and records
// what the account offered for the status endpoint. prefix is the account's, "" for a plugin executor's
// own models. With the switch off (or in Home mode) it returns models as given.
func ProvimuxOnlyMyNames(clientID, prefix string, models []*ModelInfo) []*ModelInfo {
	provimuxNames.mu.Lock()
	rule := provimuxNames.rule
	record := provimuxRecord{prefix: prefix, offered: make([]provimuxOffered, 0, len(models))}
	for _, model := range models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		record.offered = append(record.offered, provimuxOffered{id: strings.TrimSpace(model.ID), loginAlias: model.OAuthAlias != ""})
	}
	if clientID != "" {
		provimuxNames.records[clientID] = record
	}
	provimuxNames.mu.Unlock()
	if !rule.applies() {
		return models
	}
	served := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if rule.serves(model.ID, model.OAuthAlias != "", prefix) {
			served = append(served, model)
		}
	}
	return served
}

// ProvimuxNotServedMessage is "" unless the rule applies and an account offers name but has it held
// back; then it counts the refusal and returns the message that replaces "unknown provider for model".
func ProvimuxNotServedMessage(name string) string {
	name = strings.TrimSpace(name)
	key := strings.ToLower(name)
	if key == "" {
		return ""
	}
	provimuxNames.mu.Lock()
	defer provimuxNames.mu.Unlock()
	rule := provimuxNames.rule
	if !rule.applies() || !provimuxHeldLocked(rule, key) {
		return ""
	}
	entry, ok := provimuxNames.refused[name]
	switch {
	case ok:
		entry.requests++
		entry.lastAt = provimuxNames.now()
	case len(provimuxNames.refused) >= provimuxRefusedLimit:
		provimuxNames.truncated = true
	default:
		provimuxNames.refused[name] = &provimuxRefusedEntry{name: name, requests: 1, lastAt: provimuxNames.now()}
	}
	return fmt.Sprintf("model %s is not served: this Provimux serves only the names its owner set (models.only-my-names). Keep it on the console's Models page, or ask for a served name (GET /v1/models).", name)
}

func provimuxHeldLocked(rule *ProvimuxNamesRule, key string) bool {
	for _, record := range provimuxNames.records {
		for _, offered := range record.offered {
			if strings.ToLower(offered.id) == key && !rule.serves(offered.id, offered.loginAlias, record.prefix) {
				return true
			}
		}
	}
	return false
}

// ProvimuxOtherName is a name accounts offer that the rule does not serve, with those accounts
// (client IDs, in the order given to ProvimuxNamesState).
type ProvimuxOtherName struct {
	Name      string
	ClientIDs []string
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

// ProvimuxNamesSnapshot is the rule and what the given accounts offer.
type ProvimuxNamesSnapshot struct {
	Enabled     bool
	Home        bool
	Names       []ProvimuxOwnName
	KeepIgnored []ProvimuxKeepIgnored
	Others      []ProvimuxOtherName
	// Refused is nil unless the rule applies.
	Refused *ProvimuxRefused
}

// ProvimuxNamesState is the rule and the records of the given clients (the enabled accounts), then
// those of plugin executors' own models (client IDs "plugin:<plugin>:<provider>:executor"); the
// records of other clients are dropped.
func ProvimuxNamesState(clientIDs []string) ProvimuxNamesSnapshot {
	provimuxNames.mu.Lock()
	defer provimuxNames.mu.Unlock()
	rule := provimuxNames.rule
	snapshot := ProvimuxNamesSnapshot{}
	if rule != nil {
		snapshot.Enabled, snapshot.Home = rule.enabled, rule.home
		for _, key := range rule.order {
			entry := rule.names[key]
			snapshot.Names = append(snapshot.Names, ProvimuxOwnName{Name: entry.Name, SetBy: append([]string(nil), entry.SetBy...)})
		}
		snapshot.KeepIgnored = append(snapshot.KeepIgnored, rule.ignored...)
	}
	given := make(map[string]struct{}, len(clientIDs))
	for _, id := range clientIDs {
		given[id] = struct{}{}
	}
	for id := range provimuxNames.records {
		if _, ok := given[id]; !ok && !strings.HasPrefix(id, "plugin:") {
			delete(provimuxNames.records, id)
		}
	}
	others := make(map[string]*ProvimuxOtherName)
	var order []string
	var plugins []string
	for id := range provimuxNames.records {
		if _, ok := given[id]; !ok && strings.HasPrefix(id, "plugin:") {
			plugins = append(plugins, id)
		}
	}
	sort.Strings(plugins)
	for _, id := range append(append([]string(nil), clientIDs...), plugins...) {
		record, ok := provimuxNames.records[id]
		if !ok {
			continue
		}
		seen := make(map[string]struct{}, len(record.offered))
		for _, offered := range record.offered {
			if rule.serves(offered.id, offered.loginAlias, record.prefix) {
				continue
			}
			key := strings.ToLower(offered.id)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			other, okOther := others[key]
			if !okOther {
				other = &ProvimuxOtherName{Name: offered.id}
				others[key] = other
				order = append(order, key)
			}
			other.ClientIDs = append(other.ClientIDs, id)
		}
	}
	sort.Strings(order)
	for _, key := range order {
		snapshot.Others = append(snapshot.Others, *others[key])
	}
	if rule.applies() {
		refused := &ProvimuxRefused{Since: provimuxNames.since, Truncated: provimuxNames.truncated, Names: []ProvimuxRefusedName{}}
		for _, entry := range provimuxNames.refused {
			refused.Names = append(refused.Names, ProvimuxRefusedName{Name: entry.name, Requests: entry.requests, LastAt: entry.lastAt})
		}
		sort.Slice(refused.Names, func(i, j int) bool {
			if !refused.Names[i].LastAt.Equal(refused.Names[j].LastAt) {
				return refused.Names[i].LastAt.After(refused.Names[j].LastAt)
			}
			return refused.Names[i].Name < refused.Names[j].Name
		})
		snapshot.Refused = refused
	}
	return snapshot
}

// ResetProvimuxNames drops the rule, the records and the refusal counts. For tests.
func ResetProvimuxNames() {
	provimuxNames.mu.Lock()
	provimuxNames.rule = nil
	provimuxNames.records = make(map[string]provimuxRecord)
	provimuxNames.refused = nil
	provimuxNames.truncated = false
	provimuxNames.since = time.Time{}
	provimuxNames.mu.Unlock()
}

// SetProvimuxNamesClock replaces the clock of the refusal counter and returns a function that puts
// the previous one back. For tests.
func SetProvimuxNamesClock(now func() time.Time) func() {
	provimuxNames.mu.Lock()
	previous := provimuxNames.now
	provimuxNames.now = now
	provimuxNames.mu.Unlock()
	return func() {
		provimuxNames.mu.Lock()
		provimuxNames.now = previous
		provimuxNames.mu.Unlock()
	}
}
