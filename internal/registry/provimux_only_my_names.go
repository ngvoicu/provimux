package registry

// Provimux: models.only-my-names (design: .github/provimux/design/only-my-names.md, sections 16 to 18). The
// switch never changes what accounts register. The rule (package internal/provimux/onlymynames) is one immutable
// value built from the committed config alone; whether a name is served is a function of that value and the name.
// Here, the registry's side: putting a rule in force with the list cache cleared in the same locked section
// (SetProvimuxNamesRule), the list gate called from the two list loops of model_registry.go (provimuxHeld), the
// resolution of "auto" under a given rule, and one reading of the registrations for the status.

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	log "github.com/sirupsen/logrus"
)

// OnlyMyNames is models.only-my-names. Absent, or enabled false, is off.
type OnlyMyNames struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Keep    []string `yaml:"keep,omitempty" json:"keep,omitempty"`
}

// provimuxHeld is the list gate (G1): whether a registered model is left out of the list built for handlerType
// ("" for the lists of model infos). The caller holds the registry's lock, under which a rule is put in force, so
// one list is built under one rule. A model is listed when its id is served. The Gemini format names a model by
// Name, falling back to the id; there the name it shows, without "models/", must be served too, or the model is
// left out of that format (a plugin executor's provider-native name; an alias spelled "models/x").
func (r *ModelRegistry) provimuxHeld(info *ModelInfo, handlerType string) bool {
	rule := onlymynames.Current()
	if !rule.Applies() || info == nil {
		return false
	}
	if !rule.Serves(info.ID) {
		return true
	}
	if handlerType == "gemini" {
		shown := strings.TrimSpace(info.Name)
		if shown == "" {
			shown = strings.TrimSpace(info.ID)
		}
		if name := strings.TrimPrefix(shown, "models/"); name != strings.TrimSpace(info.ID) {
			return !rule.Serves(name)
		}
	}
	return false
}

// SetProvimuxNamesRule puts rule in force: under the registry's write lock, the store and, when the rule in force
// applies before or after, the list cache cleared, so no list built under the old rule survives the call. With
// the rule applying it warns, at every call (every reload), for each keep entry it ignores and when the config
// sets no name; with it off it logs nothing.
func SetProvimuxNamesRule(rule *onlymynames.Rule) {
	reg := GetGlobalRegistry()
	reg.mutex.Lock()
	stored, wasOn := onlymynames.Put(rule)
	if wasOn || stored.Applies() {
		reg.invalidateAvailableModelsCacheLocked()
	}
	reg.mutex.Unlock()
	if !stored.Applies() {
		return
	}
	for _, ignored := range stored.KeepIgnored() {
		log.Warnf("only-my-names: keep entry %q is ignored (%s): keep takes exact names", ignored.Value, ignored.Reason)
	}
	if len(stored.Names()) == 0 {
		log.Warn("only-my-names is on and no name is set in the config: nothing is served")
	}
}

// ProvimuxFirstServedUnder resolves "auto" under rule, as GetFirstAvailableModel resolves it from the gated
// list: the newest available model that rule serves and that has an available account; "" when there is none.
// It reads the registry (what is available) but judges every candidate with rule alone.
func ProvimuxFirstServedUnder(rule *onlymynames.Rule) string {
	reg := GetGlobalRegistry()
	type candidate struct {
		id      string
		created int64
	}
	now := reg.now()
	reg.mutex.RLock()
	candidates := make([]candidate, 0, len(reg.models))
	for _, registration := range reg.models {
		available, _ := modelRegistrationAvailability(registration, now)
		if !available || registration == nil || registration.Info == nil || !rule.Serves(registration.Info.ID) {
			continue
		}
		candidates = append(candidates, candidate{id: registration.Info.ID, created: registration.Info.Created})
	}
	reg.mutex.RUnlock()
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].created != candidates[j].created {
			return candidates[i].created > candidates[j].created
		}
		return candidates[i].id < candidates[j].id
	})
	for _, c := range candidates {
		if reg.GetModelCount(c.id) > 0 {
			return c.id
		}
	}
	return ""
}

// ProvimuxRegistered is one model one client registers.
type ProvimuxRegistered struct{ ClientID, ID string }

// ProvimuxNamesView is one consistent reading for the status: the rule in force, the refusal counter of that
// rule (nil unless it applies), and every model every client registers (accounts and plugin executors alike;
// clients sorted, each client's models in its order), all under one read lock of the registry and without
// cloning model metadata. A rule is put in force under the registry's write lock, so the three belong together.
func ProvimuxNamesView() (rule *onlymynames.Rule, registered []ProvimuxRegistered, refused *onlymynames.Refused) {
	reg := GetGlobalRegistry()
	reg.mutex.RLock()
	defer reg.mutex.RUnlock()
	rule = onlymynames.Current()
	if rule.Applies() {
		refused = onlymynames.RefusedNow()
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
		seen := make(map[string]struct{}, len(reg.clientModels[clientID]))
		for _, id := range reg.clientModels[clientID] {
			if _, dup := seen[id]; dup || id == "" {
				continue
			}
			seen[id] = struct{}{}
			registered = append(registered, ProvimuxRegistered{ClientID: clientID, ID: id})
		}
	}
	return rule, registered, refused
}

// ResetProvimuxNames drops the rule and the refusal counts, and the list cache. For tests.
func ResetProvimuxNames() {
	reg := GetGlobalRegistry()
	reg.mutex.Lock()
	onlymynames.Reset()
	reg.invalidateAvailableModelsCacheLocked()
	reg.mutex.Unlock()
}
