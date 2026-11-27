package cliproxy

import (
	"os"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: J3 of session-route.md 15.13. Every path that commits a configuration reaches the layer's hook, and the
// rule's value in use follows: the first configuration of a start (not committed), a hot reload as the watcher
// applies it, and a write through the config API the console uses, read back from the file. Each change of the
// routing settings makes a new selector, and the state reads it as it runs. Nothing is sent anywhere.
func TestProvimuxSessionReturnFollowsEveryCommit(t *testing.T) {
	t.Cleanup(func() { coreauth.ProvimuxSetSessionReturn(nil, coreauth.ProvimuxSessionReturnSetting{}) })
	h := newNamesHarness(t, "routing:\n  session-affinity: true\n  session-affinity-ttl: \"1h\"\n  session-affinity-return:\n    cold-after: \"6m\"\n")
	m := h.service.coreManager
	state := func() coreauth.ProvimuxSessionReturnState {
		return coreauth.ProvimuxSessionReturnStateOf(m, time.Now())
	}
	expect := func(when, reason string, coldAfterS float64) {
		t.Helper()
		s := state()
		got := ""
		if s.Reason != nil {
			got = *s.Reason
		}
		cold := -1.0
		if s.Setting.ColdAfterS != nil {
			cold = *s.Setting.ColdAfterS
		}
		if got != reason || s.InEffect != (reason == "") || cold != coldAfterS {
			t.Fatalf("%s: in effect %v, reason %q, cold_after_s %v; want reason %q, cold_after_s %v", when, s.InEffect, got, cold, reason, coldAfterS)
		}
	}
	reload := func(change func(*internalconfig.Config)) {
		t.Helper()
		cfg := h.service.currentConfig().CloneForRuntime()
		change(cfg)
		h.service.applyWatcherConfigUpdate(cfg)
	}

	expect("the first configuration of a start", "", 360)

	reload(func(cfg *internalconfig.Config) { cfg.Routing.SessionAffinity = false })
	expect("affinity off by reload", "session-affinity-off", 360)
	reload(func(cfg *internalconfig.Config) { cfg.Routing.SessionAffinity = true })
	expect("affinity on again by reload", "", 360)
	reload(func(cfg *internalconfig.Config) { cfg.Routing.SessionAffinityTTL = "5m" })
	expect("a binding lifetime under cold-after by reload", "not-below-ttl", 360)
	reload(func(cfg *internalconfig.Config) { cfg.Routing.SessionAffinityTTL = "1h" })
	reload(func(cfg *internalconfig.Config) { cfg.Routing.SessionAffinityRebindOn = "never" })
	expect("rebind-on never by reload", "rebind-never", 360)
	reload(func(cfg *internalconfig.Config) { cfg.Routing.SessionAffinityRebindOn = "any" })
	reload(func(cfg *internalconfig.Config) { cfg.Routing.SessionAffinityReturn = nil })
	expect("the key removed by reload", "not-set", -1)

	// The console's write: the config API puts the key in the file; the watcher applies what the file then says.
	h.put("routing/session-affinity-return", `{"cold-after":"1m0.9s"}`)
	raw, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "session-affinity-return:") || !strings.Contains(string(raw), "1m0.9s") {
		t.Fatalf("the config API did not write the key:\n%s", raw)
	}
	cfg, err := internalconfig.LoadConfig(h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	h.service.applyWatcherConfigUpdate(cfg)
	expect("written through the config API", "", 60.9)
}
