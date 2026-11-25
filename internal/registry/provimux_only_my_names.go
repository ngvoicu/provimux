package registry

// Provimux: models.only-my-names (design: .github/provimux/design/only-my-names.md). With the switch
// on, an account's model is registered only under a name the owner set; every other name is held
// back, as an excluded model is: not listed, and refused as unknown with a message that names the
// setting. The rule runs in front of the registry, in the three places accounts hand it their models
// (ProvimuxOnlyMyNames), so every list and every request path follows without an edit of its own.
//
// Locking: one mutex, provimuxNames.mu, guards the rule, the records and the refusal counter, for
// writers and readers alike. Under it the registry is only read (its own read lock, never held by a
// caller of this file), so the order is always provimuxNames.mu, then the registry's lock.

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

// provimuxPluginClientPrefix starts the registry client ID of a plugin executor's own models.
const provimuxPluginClientPrefix = "plugin:"

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
// (alias rows and keep; compared trimmed and without case), the keep entries it ignores and the hash
// of the config file it was read from. A login's own aliases are not here: they belong to that login
// and are recognised on the model (OAuthAlias).
type ProvimuxNamesRule struct {
	enabled bool
	home    bool
	source  string
	names   map[string]*ProvimuxOwnName
	order   []string
	ignored []ProvimuxKeepIgnored
}

// NewProvimuxNamesRule returns an empty rule. home: the gateway is in Home mode, where Home decides
// model lists and routing and the rule does not apply.
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

// applies reports whether the rule holds names back: the switch is on and not in Home mode.
func (r *ProvimuxNamesRule) applies() bool { return r != nil && r.enabled && !r.home }

func (r *ProvimuxNamesRule) has(key string) bool {
	if r == nil || key == "" {
		return false
	}
	_, ok := r.names[key]
	return ok
}

// serves reports whether the rule's names serve a model registered as id on an account with prefix:
// a login alias (loginAlias: OAuthAlias is set; its prefixed copies too), a name of the config, or
// "<prefix>/<name>" for a name of the config. It ignores the switch. Without a rule nothing is a name.
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

// registers is what an account with this record registers under the rule: everything with the
// switch off, the names it serves with it on. Lower-case ids.
func (r *ProvimuxNamesRule) registers(record provimuxRecord) map[string]struct{} {
	out := make(map[string]struct{}, len(record.offered))
	for _, offered := range record.offered {
		if !r.applies() || r.serves(offered.id, offered.loginAlias, record.prefix) {
			out[strings.ToLower(offered.id)] = struct{}{}
		}
	}
	return out
}

// provimuxOffered is one model an account handed the registry, before the rule.
type provimuxOffered struct {
	id         string
	loginAlias bool
}

// provimuxRecord is what an account offered at its last registration through a writer, its prefix,
// and the registry's epoch for it just before (epoch). Every registration and every removal moves
// that epoch by one, and every writer publishes once after recording, so an epoch past epoch+1
// means the account was removed or emptied since by a path that does not pass a writer (removal,
// disabling, every model excluded, a plugin unloaded): the record is stale and is dropped.
type provimuxRecord struct {
	prefix  string
	offered []provimuxOffered
	epoch   uint64
}

type provimuxRefusedEntry struct {
	name     string
	requests int
	lastAt   time.Time
}

var provimuxNames = struct {
	mu         sync.Mutex
	rule       *ProvimuxNamesRule // the current rule: writers apply it
	generation uint64             // counts rules set
	applied    *ProvimuxNamesRule // the last rule the registry was seen to match; the status reports it
	appliedGen uint64
	records    map[string]provimuxRecord
	gcAt       int // the record count that starts the next sweep of stale records
	refused    map[string]*provimuxRefusedEntry
	since      time.Time
	truncated  bool
	now        func() time.Time
}{records: make(map[string]provimuxRecord), gcAt: 64, now: time.Now}

// SetProvimuxNamesRule installs the rule of a committed config. Counting refusals starts again when
// the rule starts applying. It warns, at every call (every reload), with the switch on and no name
// in the config, and for each keep entry it ignores.
func SetProvimuxNamesRule(rule *ProvimuxNamesRule) {
	provimuxNames.mu.Lock()
	wasOn := provimuxNames.rule.applies()
	provimuxNames.rule = rule
	provimuxNames.generation++
	if provimuxNames.applied == nil {
		provimuxNames.applied, provimuxNames.appliedGen = rule, provimuxNames.generation
	}
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
	if rule == nil {
		return
	}
	for _, ignored := range rule.ignored {
		log.Warnf("only-my-names: keep entry %q is ignored (%s): keep takes exact names", ignored.Value, ignored.Reason)
	}
	if rule.applies() && len(rule.names) == 0 {
		log.Warn("only-my-names is on and no name is set in the config: only aliases in a login's own file are served")
	}
}

// ProvimuxOnlyMyNames returns the models of clientID that are served under the current rule and records
// what the account offered for the status endpoint. prefix is the account's, "" for a plugin executor's
// own models. With the switch off (or in Home mode) it returns models as given. An account that offers
// nothing leaves no record.
func ProvimuxOnlyMyNames(clientID, prefix string, models []*ModelInfo) []*ModelInfo {
	provimuxNames.mu.Lock()
	rule := provimuxNames.rule
	if clientID != "" {
		record := provimuxRecord{prefix: prefix, offered: make([]provimuxOffered, 0, len(models))}
		for _, model := range models {
			if model == nil || strings.TrimSpace(model.ID) == "" {
				continue
			}
			record.offered = append(record.offered, provimuxOffered{id: strings.TrimSpace(model.ID), loginAlias: model.OAuthAlias != ""})
		}
		if len(record.offered) == 0 {
			delete(provimuxNames.records, clientID)
		} else {
			record.epoch = GetGlobalRegistry().ClientRegistrationEpoch(clientID)
			provimuxNames.records[clientID] = record
			provimuxSweepLocked(false)
		}
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

// provimuxStaleLocked reports whether the record of clientID no longer describes the account.
func provimuxStaleLocked(clientID string, record provimuxRecord) bool {
	return GetGlobalRegistry().ClientRegistrationEpoch(clientID) > record.epoch+1
}

// provimuxSweepLocked drops stale records. Unless forced it runs only when the records have doubled
// since the last sweep, so its cost per registration stays constant and the records stay within
// twice the accounts that exist.
func provimuxSweepLocked(force bool) {
	if !force && len(provimuxNames.records) < provimuxNames.gcAt {
		return
	}
	for id, record := range provimuxNames.records {
		if provimuxStaleLocked(id, record) {
			delete(provimuxNames.records, id)
		}
	}
	provimuxNames.gcAt = max(64, 2*len(provimuxNames.records))
}

// provimuxHeldLocked reports whether an account that still exists offers name (lower case) without
// registering it.
func provimuxHeldLocked(key string) bool {
	for id, record := range provimuxNames.records {
		if provimuxStaleLocked(id, record) {
			continue
		}
		for _, offered := range record.offered {
			if strings.ToLower(offered.id) == key {
				if _, registered := provimuxRegisteredLocked(id)[key]; !registered {
					return true
				}
			}
		}
	}
	return false
}

// provimuxRegisteredLocked is what the registry holds for clientID now, lower-case ids.
func provimuxRegisteredLocked(clientID string) map[string]struct{} {
	models := GetGlobalRegistry().GetModelsForClient(clientID)
	out := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model != nil {
			out[strings.ToLower(strings.TrimSpace(model.ID))] = struct{}{}
		}
	}
	return out
}

// ProvimuxRouteHeld reports whether name (as the gateway resolved it, a thinking suffix already cut)
// is held back now: the rule applies and an account offers the name without registering it. Nothing
// is counted. A plugin model router's decision to run such a name on a plugin executor is dropped.
func ProvimuxRouteHeld(name string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return false
	}
	provimuxNames.mu.Lock()
	defer provimuxNames.mu.Unlock()
	if !provimuxNames.rule.applies() || len(GetGlobalRegistry().GetModelProviders(name)) > 0 {
		return false
	}
	return provimuxHeldLocked(key)
}

// ProvimuxNotServedMessage is "" unless the rule applies and an account offers name but has it held
// back; then it counts the refusal and returns the message that replaces "unknown provider for model".
// The name is the one the gateway resolved (speech and video routes map tts-1 or sora-2 to a fixed name
// first), without a thinking suffix.
func ProvimuxNotServedMessage(name string) string {
	name = strings.TrimSpace(name)
	key := strings.ToLower(name)
	if key == "" {
		return ""
	}
	provimuxNames.mu.Lock()
	defer provimuxNames.mu.Unlock()
	if !provimuxNames.rule.applies() || !provimuxHeldLocked(key) {
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

// ProvimuxOtherName is a name accounts offer that no account serves under the rule, with those
// accounts (client IDs, in the order given to ProvimuxNamesState, then plugin executors).
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

// ProvimuxApplied says which rule the registry was last seen to match, and whether a change is still
// on its way (a newer rule, or a registration recorded and not yet published).
type ProvimuxApplied struct {
	Generation uint64
	Source     string // the SHA-256 of the config file of the applied rule; "" when not known
	Applying   bool
}

// ProvimuxNamesSnapshot is the rule as applied, what the given accounts offer, and the marker.
type ProvimuxNamesSnapshot struct {
	Enabled     bool
	Home        bool
	Names       []ProvimuxOwnName
	KeepIgnored []ProvimuxKeepIgnored
	Others      []ProvimuxOtherName
	// Refused is nil unless the applied rule applies.
	Refused *ProvimuxRefused
	Applied ProvimuxApplied
	// Plugins are the plugin executor clients that hold a record, sorted.
	Plugins []string
}

// provimuxAppliedLocked compares the registry with what the current rule says each existing account
// registers (clientIDs, then every plugin executor), drops stale records, and moves the applied rule
// to the current one when they all match. It returns the clients it looked at.
func provimuxAppliedLocked(clientIDs []string) ([]string, ProvimuxApplied) {
	provimuxSweepLocked(true)
	ids := append([]string(nil), clientIDs...)
	given := make(map[string]struct{}, len(clientIDs))
	for _, id := range clientIDs {
		given[id] = struct{}{}
	}
	var plugins []string
	for id := range provimuxNames.records {
		if _, ok := given[id]; !ok && strings.HasPrefix(id, provimuxPluginClientPrefix) {
			plugins = append(plugins, id)
		}
	}
	sort.Strings(plugins)
	ids = append(ids, plugins...)

	rule := provimuxNames.rule
	matches := true
	for _, id := range ids {
		record, ok := provimuxNames.records[id]
		if !ok {
			continue
		}
		want, have := rule.registers(record), provimuxRegisteredLocked(id)
		if len(want) != len(have) {
			matches = false
			break
		}
		for key := range want {
			if _, ok := have[key]; !ok {
				matches = false
				break
			}
		}
		if !matches {
			break
		}
	}
	if matches {
		provimuxNames.applied, provimuxNames.appliedGen = rule, provimuxNames.generation
	}
	applied := ProvimuxApplied{Generation: provimuxNames.appliedGen, Applying: !matches || provimuxNames.appliedGen != provimuxNames.generation}
	if provimuxNames.applied != nil {
		applied.Source = provimuxNames.applied.source
	}
	return ids, applied
}

// ProvimuxNamesApplied is the marker alone, for GET /provimux/model-routes. clientIDs are the enabled
// accounts.
func ProvimuxNamesApplied(clientIDs []string) ProvimuxApplied {
	provimuxNames.mu.Lock()
	defer provimuxNames.mu.Unlock()
	_, applied := provimuxAppliedLocked(clientIDs)
	return applied
}

// ProvimuxNamesState is the rule as applied and the records of the given clients (the enabled
// accounts) and of plugin executors' own models. A name is in Others only when no account serves it
// under the applied rule's names (a login's own alias counts on its login) and no account registers
// it now: a served name is never reported as not served.
func ProvimuxNamesState(clientIDs []string) ProvimuxNamesSnapshot {
	provimuxNames.mu.Lock()
	defer provimuxNames.mu.Unlock()
	ids, applied := provimuxAppliedLocked(clientIDs)
	rule := provimuxNames.applied
	snapshot := ProvimuxNamesSnapshot{Applied: applied}
	if rule != nil {
		snapshot.Enabled, snapshot.Home = rule.enabled, rule.home
		for _, key := range rule.order {
			entry := rule.names[key]
			snapshot.Names = append(snapshot.Names, ProvimuxOwnName{Name: entry.Name, SetBy: append([]string(nil), entry.SetBy...)})
		}
		snapshot.KeepIgnored = append(snapshot.KeepIgnored, rule.ignored...)
	}
	for _, id := range ids {
		if strings.HasPrefix(id, provimuxPluginClientPrefix) {
			if _, ok := provimuxNames.records[id]; ok {
				snapshot.Plugins = append(snapshot.Plugins, id)
			}
		}
	}

	// Names some account serves under the rule, or (with the rule applying) registers now.
	served := map[string]struct{}{}
	for _, id := range ids {
		record, ok := provimuxNames.records[id]
		if !ok {
			continue
		}
		for _, offered := range record.offered {
			if rule.serves(offered.id, offered.loginAlias, record.prefix) {
				served[strings.ToLower(offered.id)] = struct{}{}
			}
		}
		if rule.applies() { // with the switch off every name is registered; others is the preview
			for key := range provimuxRegisteredLocked(id) {
				served[key] = struct{}{}
			}
		}
	}
	others := make(map[string]*ProvimuxOtherName)
	var order []string
	for _, id := range ids {
		record, ok := provimuxNames.records[id]
		if !ok {
			continue
		}
		seen := make(map[string]struct{}, len(record.offered))
		for _, offered := range record.offered {
			key := strings.ToLower(offered.id)
			if _, isServed := served[key]; isServed {
				continue
			}
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
	if rule.applies() && provimuxNames.refused != nil {
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

// ProvimuxNamesRecords is how many records are held. For tests.
func ProvimuxNamesRecords() int {
	provimuxNames.mu.Lock()
	defer provimuxNames.mu.Unlock()
	return len(provimuxNames.records)
}

// ResetProvimuxNames drops the rule, the records and the refusal counts. For tests.
func ResetProvimuxNames() {
	provimuxNames.mu.Lock()
	provimuxNames.rule = nil
	provimuxNames.applied = nil
	provimuxNames.generation, provimuxNames.appliedGen = 0, 0
	provimuxNames.records = make(map[string]provimuxRecord)
	provimuxNames.gcAt = 64
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
