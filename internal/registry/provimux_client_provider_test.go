package registry

import "testing"

func TestProvimuxClientProvider(t *testing.T) {
	r := GetGlobalRegistry()
	r.RegisterClient("provimux-kimi-login", "kimi", []*ModelInfo{{ID: "kimi-k2.6"}})
	t.Cleanup(func() { r.UnregisterClient("provimux-kimi-login") })
	if got := ProvimuxClientProvider("provimux-kimi-login"); got != "kimi" {
		t.Fatalf("provider %q, want kimi", got)
	}
	if ProvimuxClientProvider("provimux-unknown") != "" || ProvimuxClientProvider("") != "" {
		t.Fatal("an unknown client has no provider")
	}
}
