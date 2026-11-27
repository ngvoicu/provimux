package cliproxy

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Finding 1: bindings on an account without an identity are counted, so the state says why they do not come back.
func TestProvimuxAffinityLeftOutNoIdentity(t *testing.T) {
	dir := t.TempDir()
	a := pmxClaude("provimux-test-a.json", "acc-a")
	noOrg := &coreauth.Auth{ID: "provimux-test-no-org.json", Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_uuid": "acc-k"}} // no organisation
	s := pmxService(t, dir, "", nil, a, noOrg)
	pmxStart(t, s)
	pmxBindOn(t, s, "provimux-test-on-a", a, a, noOrg)
	pmxBindOn(t, s, "provimux-test-no-identity-1", noOrg, a, noOrg)
	pmxBindOn(t, s, "provimux-test-no-identity-2", noOrg, a, noOrg)
	status := pmxWrite(s)
	snapshot, key := pmxPaths(t, s)
	sessions := pmxSessions(t, snapshot, key)
	var onA, onNoOrg int
	for _, session := range sessions {
		if session == "header:provimux-test-no-identity-1" || session == "header:provimux-test-no-identity-2" {
			onNoOrg++
		}
		if session == "header:provimux-test-on-a" {
			onA++
		}
	}
	if onA != 1 || onNoOrg != 0 {
		t.Fatalf("written %v", sessions)
	}
	// Probes bound on the login without an identity count too.
	if !status.InEffect || status.LeftOut != 0 || status.LeftOutNoIdentity < 2 {
		t.Fatalf("state %+v", status)
	}
	if status.LastBindings+status.LeftOutNoIdentity != len(pmxAllBindings(s)) {
		t.Fatalf("written %d + without identity %d != %d bindings in memory", status.LastBindings, status.LeftOutNoIdentity, len(pmxAllBindings(s)))
	}
	s.provimuxAffinityStop()
}

// pmxAllBindings lists the sessions of the selector's cache.
func pmxAllBindings(s *Service) []string {
	state, err := coreauth.ProvimuxAffinityCapture(s.coreManager.Selector(), func(string, time.Time) (string, bool) { return "x", true }, time.Now())
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(state.Sessions))
	for _, session := range state.Sessions {
		out = append(out, session.SessionID)
	}
	return out
}
