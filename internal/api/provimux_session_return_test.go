package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: GET /v8/management/provimux/session-return, the rule's state (session-route.md 15.6, rows 11 and 12 of
// 15.10), through the real management router behind the management key, in each mode, against the contract; and the
// show's rule, which says the same in two fields. Every account and key is invented.

func srReturnSetting(cold, ttl time.Duration) auth.ProvimuxSessionReturnSetting {
	return auth.ProvimuxSessionReturnSetting{ColdAfter: cold, ColdAfterSet: cold > 0, Affinity: true, TTL: ttl, Strategy: "round-robin", Subagents: true, RebindOn: "any"}
}

func TestProvimuxSessionReturnStatePath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	contract := loadSRContract(t)
	for _, mode := range []struct {
		name    string
		prepare func(t *testing.T, m *auth.Manager)
		reason  string
		cold    any
	}{
		{name: "not set (as installed)", reason: "not-set", cold: nil, prepare: func(t *testing.T, m *auth.Manager) {
			t.Cleanup(auth.SetProvimuxSessionReturnForTest(m, srReturnSetting(0, time.Hour)))
		}},
		{name: "in effect", reason: "", cold: 360.0, prepare: func(t *testing.T, m *auth.Manager) {
			t.Cleanup(auth.SetProvimuxSessionReturnForTest(m, srReturnSetting(6*time.Minute, time.Hour)))
		}},
		{name: "not below the TTL", reason: "not-below-ttl", cold: 7200.0, prepare: func(t *testing.T, m *auth.Manager) {
			t.Cleanup(auth.SetProvimuxSessionReturnForTest(m, srReturnSetting(2*time.Hour, time.Hour)))
		}},
		{name: "rebind-on never", reason: "rebind-never", cold: 360.0, prepare: func(t *testing.T, m *auth.Manager) {
			never := auth.NewSessionAffinitySelectorWithConfig(auth.SessionAffinityConfig{Fallback: &auth.RoundRobinSelector{}, TTL: time.Hour, RebindOn: "never"})
			t.Cleanup(never.Stop)
			m.SetSelector(never)
			setting := srReturnSetting(6*time.Minute, time.Hour)
			setting.RebindOn = "never"
			t.Cleanup(auth.SetProvimuxSessionReturnForTest(m, setting))
		}},
		{name: "session affinity off", reason: "session-affinity-off", cold: 360.0, prepare: func(t *testing.T, m *auth.Manager) {
			m.SetSelector(&auth.FillFirstSelector{})
			setting := srReturnSetting(6*time.Minute, time.Hour)
			setting.Affinity = false
			t.Cleanup(auth.SetProvimuxSessionReturnForTest(m, setting))
		}},
		{name: "plugin scheduler", reason: "plugin-scheduler", cold: 360.0, prepare: func(t *testing.T, m *auth.Manager) {
			m.SetPluginScheduler(srAPIScheduler{})
			t.Cleanup(auth.SetProvimuxSessionReturnForTest(m, srReturnSetting(6*time.Minute, time.Hour)))
		}},
		{name: "Home mode", reason: "home-mode", cold: 360.0, prepare: func(t *testing.T, m *auth.Manager) {
			cfg := &proxyconfig.Config{}
			cfg.Home.Enabled = true
			m.SetConfig(cfg)
			setting := srReturnSetting(6*time.Minute, time.Hour)
			setting.Home = true
			t.Cleanup(auth.SetProvimuxSessionReturnForTest(m, setting))
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			s := provimuxTestServer(t, true, false)
			m := auth.NewManager(nil, nil, nil)
			affinity := auth.NewSessionAffinitySelectorWithConfig(auth.SessionAffinityConfig{Fallback: &auth.RoundRobinSelector{}, TTL: time.Hour})
			t.Cleanup(affinity.Stop)
			m.SetSelector(affinity)
			s.mgmt.SetAuthManager(m)
			mode.prepare(t, m)
			recorder := provimuxServe(s, "/v8/management/provimux/session-return", true)
			body := recorder.Body.Bytes()
			contract.check(t, "SessionReturnState", recorder.Code, http.StatusOK, body)
			t.Logf("ANSWER %s: %s", mode.name, body)
			var state struct {
				InEffect bool    `json:"in_effect"`
				Reason   *string `json:"reason"`
				Setting  struct {
					ColdAfterS any `json:"cold_after_s"`
				} `json:"setting"`
			}
			mustUnmarshal(t, body, &state)
			reason := ""
			if state.Reason != nil {
				reason = *state.Reason
			}
			if reason != mode.reason || state.InEffect != (mode.reason == "") || state.Setting.ColdAfterS != mode.cold {
				t.Fatalf("state %s; want reason %q, cold_after_s %v", body, mode.reason, mode.cold)
			}
			if recorder := provimuxServe(s, "/v8/management/provimux/session-return", false); recorder.Code != http.StatusUnauthorized {
				t.Fatalf("without the management key: %d", recorder.Code)
			}
		})
	}
}

// TestProvimuxSessionReturnShowRule: the show's rule says what the state says, in two fields; unset, it is the bytes
// part one wrote ({"in_effect":false,"cold_after_s":null}).
func TestProvimuxSessionReturnShowRule(t *testing.T) {
	contract := loadSRContract(t)
	for _, set := range []bool{false, true} {
		f := newSRAPIFixture(t)
		if set {
			t.Cleanup(auth.SetProvimuxSessionReturnForTest(f.manager, srReturnSetting(6*time.Minute, time.Hour)))
		}
		header := http.Header{"X-Session-Affinity": {"pi-show-rule"}}
		f.turn("sk-sr-one", header)
		code, body := f.show("sk-sr-one", header, `{"model":"`+srAPIModel+`"}`)
		contract.check(t, "SessionRoute", code, http.StatusOK, body)
		var show struct {
			Rule json.RawMessage `json:"rule"`
		}
		mustUnmarshal(t, body, &show)
		want := `{"in_effect":false,"cold_after_s":null}`
		if set {
			want = `{"in_effect":true,"cold_after_s":360}`
		}
		if string(show.Rule) != want {
			t.Fatalf("set %v: rule %s, want %s", set, show.Rule, want)
		}
	}
}

// TestProvimuxSessionReturnContractExamples: the state's examples in the contract are valid against its schema and go
// through the build's own type unchanged.
func TestProvimuxSessionReturnContractExamples(t *testing.T) {
	contract := loadSRContract(t)
	for _, name := range []string{"ReturnInEffect", "ReturnAsInstalled", "ReturnNotSet", "ReturnNotBelowTTL"} {
		example := contract.example(t, name)
		contract.check(t, "SessionReturnState", http.StatusOK, http.StatusOK, example)
		var state auth.ProvimuxSessionReturnState
		mustUnmarshal(t, example, &state)
		back, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, example, back) {
			t.Fatalf("example %s through the build's type:\n%s\n%s", name, example, back)
		}
	}
}
