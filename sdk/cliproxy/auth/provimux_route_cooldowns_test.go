package auth

import (
	"reflect"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

var provimuxCooldownNow = time.Date(2026, 11, 19, 18, 0, 0, 0, time.UTC)

func provimuxQuotaState(until time.Time) *ModelState {
	return &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: until,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: until, BackoffLevel: 1},
	}
}

func provimuxOAuthAuth(id string, states map[string]*ModelState) *Auth {
	return &Auth{
		ID:          id,
		Provider:    "claude",
		Status:      StatusActive,
		Attributes:  map[string]string{AttributeAuthKind: AuthKindOAuth},
		ModelStates: states,
	}
}

// provimuxSchedulerBlocks reports whether the scheduler's own projection suspends routeModel with a deadline.
func provimuxSchedulerBlocks(m *Manager, auth *Auth, routeModel string) bool {
	projection := m.clientModelProjectionForAuth(auth, routeModel, provimuxCooldownNow)
	return projection.Suspended && !projection.SuspendUntil.IsZero()
}

func TestProvimuxRouteCooldownsCutsThePrefix(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := provimuxOAuthAuth("pmx-prefix", map[string]*ModelState{"m": provimuxQuotaState(provimuxCooldownNow.Add(10 * time.Minute))})
	auth.Prefix = "team"
	got := m.ProvimuxRouteCooldowns(auth, []string{"team/m", "team/other"}, provimuxCooldownNow)
	view, ok := got["team/m"]
	if !ok || view.Scope != "model" || view.Reason != "quota" || !view.RetryAt.Equal(provimuxCooldownNow.Add(10*time.Minute)) {
		t.Fatalf("team/m cooldown = %+v (found %v), want model-scope quota until +10m", view, ok)
	}
	if view.RemainingSeconds != 600 {
		t.Fatalf("remaining = %d, want 600", view.RemainingSeconds)
	}
	if _, ok := got["team/other"]; ok {
		t.Fatalf("team/other has a cooldown: %+v", got["team/other"])
	}
	for _, model := range []string{"team/m", "team/other"} {
		if _, found := got[model]; found != provimuxSchedulerBlocks(m, auth, model) {
			t.Fatalf("%s: cooldown found %v, scheduler blocks %v", model, found, !found)
		}
	}
}

func TestProvimuxRouteCooldownsResolvesOAuthAlias(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"claude": {{Name: "claude-opus-5-5", Alias: "opus"}}})
	auth := provimuxOAuthAuth("pmx-alias", map[string]*ModelState{"claude-opus-5-5": provimuxQuotaState(provimuxCooldownNow.Add(time.Hour))})
	got := m.ProvimuxRouteCooldowns(auth, []string{"opus"}, provimuxCooldownNow)
	if view, ok := got["opus"]; !ok || view.Scope != "model" {
		t.Fatalf("opus cooldown = %+v (found %v), want the state of claude-opus-5-5", view, ok)
	}
	if !provimuxSchedulerBlocks(m, auth, "opus") {
		t.Fatal("the scheduler does not block opus; the fixture is wrong")
	}
}

func TestProvimuxRouteCooldownsIgnoresSuffixAndCase(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := provimuxOAuthAuth("pmx-case", map[string]*ModelState{"M": provimuxQuotaState(provimuxCooldownNow.Add(time.Minute))})
	got := m.ProvimuxRouteCooldowns(auth, []string{"m(high)", "m"}, provimuxCooldownNow)
	for _, model := range []string{"m(high)", "m"} {
		if _, ok := got[model]; !ok {
			t.Fatalf("%s: no cooldown, want the state under M", model)
		}
		if !provimuxSchedulerBlocks(m, auth, model) {
			t.Fatalf("%s: the scheduler does not block it", model)
		}
	}
}

func TestProvimuxRouteCooldownsLaterRetryWins(t *testing.T) {
	m := NewManager(nil, nil, nil)
	for _, tc := range []struct {
		name            string
		credential, mdl time.Duration
		wantScope       string
	}{
		{"model later", 5 * time.Minute, 20 * time.Minute, "model"},
		{"credential later", 30 * time.Minute, 20 * time.Minute, "credential"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := provimuxOAuthAuth("pmx-both", map[string]*ModelState{"m": provimuxQuotaState(provimuxCooldownNow.Add(tc.mdl))})
			auth.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: provimuxCooldownNow.Add(tc.credential)}
			got := m.ProvimuxRouteCooldowns(auth, []string{"m", "n"}, provimuxCooldownNow)
			if view := got["m"]; view.Scope != tc.wantScope {
				t.Fatalf("m cooldown scope = %q, want %q (%+v)", view.Scope, tc.wantScope, view)
			}
			if view, ok := got["n"]; !ok || view.Scope != "credential" || view.Reason != "credential_quota" {
				t.Fatalf("n cooldown = %+v (found %v), want the credential's", view, ok)
			}
			for _, model := range []string{"m", "n"} {
				if !provimuxSchedulerBlocks(m, auth, model) {
					t.Fatalf("%s: the scheduler does not block it", model)
				}
			}
		})
	}
}

func TestProvimuxRouteCooldownsSkipsExpired(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := provimuxOAuthAuth("pmx-expired", map[string]*ModelState{"m": provimuxQuotaState(provimuxCooldownNow.Add(-time.Minute))})
	if got := m.ProvimuxRouteCooldowns(auth, []string{"m"}, provimuxCooldownNow); len(got) != 0 {
		t.Fatalf("expired state returned: %+v", got)
	}
	if provimuxSchedulerBlocks(m, auth, "m") {
		t.Fatal("the scheduler blocks an expired state")
	}
}

func provimuxPooledManager(t *testing.T) (*Manager, *Auth) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
		Name:    "pool",
		Prefix:  "tenant",
		BaseURL: "https://example.com/v1",
		Models: []internalconfig.OpenAICompatibilityModel{
			{Name: "upstream-a", Alias: "public-model"},
			{Name: "upstream-b", Alias: "public-model"},
		},
	}}})
	auth := &Auth{
		ID:       "pmx-pool",
		Provider: "openai-compatibility:pool",
		Prefix:   "tenant",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeSource: "config:pool[0]",
			"compat_name":   "pool",
			"provider_key":  "openai-compatibility:pool",
		},
	}
	registerCapabilityTestAuth(t, m, auth)
	registered, ok := m.GetByID(auth.ID)
	if !ok {
		t.Fatal("pooled auth not registered")
	}
	registered.ModelStates = map[string]*ModelState{"upstream-a": provimuxQuotaState(provimuxCooldownNow.Add(time.Hour))}
	return m, registered
}

// A pooled alias has no state of its own: it shows no cooldown, as the registry shows it available.
func TestProvimuxRouteCooldownsPooledAliasHasNone(t *testing.T) {
	m, auth := provimuxPooledManager(t)
	if got := m.ProvimuxRouteCooldowns(auth, []string{"tenant/public-model"}, provimuxCooldownNow); len(got) != 0 {
		t.Fatalf("pooled alias cooldown = %+v, want none", got)
	}
	if provimuxSchedulerBlocks(m, auth, "tenant/public-model") {
		t.Fatal("the scheduler blocks the pooled alias; the pinned behaviour changed")
	}
}

// provimuxPoolOffsets copies the manager's round-robin positions.
func provimuxPoolOffsets(m *Manager) map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int, len(m.modelPoolOffsets))
	for key, offset := range m.modelPoolOffsets {
		out[key] = offset
	}
	return out
}

// One status read must not move the round-robin position of a pooled alias nor touch any state:
// a single read is enough for a rotating resolver to change the next upstream model.
func TestProvimuxRouteReadsChangeNothing(t *testing.T) {
	plain, plainAuth := provimuxPooledManager(t)
	probed, probedAuth := provimuxPooledManager(t)
	for i := 0; i < 3; i++ {
		plain.ResolveExecutionModel(plainAuth, "tenant/public-model")
		probed.ResolveExecutionModel(probedAuth, "tenant/public-model")
	}
	before := probedAuth.Clone()
	offsets := provimuxPoolOffsets(probed)
	probed.ProvimuxRouteCooldowns(probedAuth, []string{"tenant/public-model", "tenant/upstream-a"}, provimuxCooldownNow)
	if after := provimuxPoolOffsets(probed); !reflect.DeepEqual(offsets, after) {
		t.Fatalf("pool offsets changed by one read: %v -> %v", offsets, after)
	}
	if !reflect.DeepEqual(before.ModelStates, probedAuth.ModelStates) || !reflect.DeepEqual(before.Quota, probedAuth.Quota) {
		t.Fatal("a status read changed the credential's state")
	}
	if got, want := probed.ResolveExecutionModel(probedAuth, "tenant/public-model"), plain.ResolveExecutionModel(plainAuth, "tenant/public-model"); got != want {
		t.Fatalf("next upstream after one read = %q, without it %q", got, want)
	}
	first := plain.ResolveExecutionModel(plainAuth, "tenant/public-model")
	if second := plain.ResolveExecutionModel(plainAuth, "tenant/public-model"); first == second {
		t.Fatalf("pool does not rotate (%q twice); the fixture is wrong", first)
	}
}
