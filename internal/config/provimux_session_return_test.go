package config

import (
	"strings"
	"testing"
	"time"
)

// Provimux: routing.session-affinity-return (.github/provimux/design/session-route.md 15.4): one key, off unless set; a
// value that is not a duration of at least one minute is refused where the configuration is read, with the key named;
// the value is kept as the duration it was read as, 1m0.9s included.

func provimuxReturnYAML(value string) []byte {
	return []byte("config-version: 8\nrouting:\n  session-affinity: true\n  session-affinity-return:\n    cold-after: " + value + "\n")
}

func TestProvimuxSessionReturnSetting(t *testing.T) {
	for _, row := range []struct {
		value string
		want  time.Duration
	}{
		{value: `"6m"`, want: 6 * time.Minute},
		{value: `"1m"`, want: time.Minute},
		{value: `"1m0.9s"`, want: time.Minute + 900*time.Millisecond},
		{value: `" 90m "`, want: 90 * time.Minute},
		{value: `"2562047h47m16.854775807s"`, want: time.Duration(1<<63 - 1)},
	} {
		cfg, err := ParseConfigBytes(provimuxReturnYAML(row.value))
		if err != nil {
			t.Fatalf("%s: %v", row.value, err)
		}
		got, set := cfg.ProvimuxSessionReturnColdAfter()
		if !set || got != row.want {
			t.Fatalf("%s: %v %v, want %v", row.value, got, set, row.want)
		}
		if clone := cfg.CloneForRuntime(); clone.Routing.SessionAffinityReturn == nil || clone.Routing.SessionAffinityReturn.ColdAfter != cfg.Routing.SessionAffinityReturn.ColdAfter {
			t.Fatalf("%s: the runtime clone lost the setting", row.value)
		}
	}
}

func TestProvimuxSessionReturnSettingRefused(t *testing.T) {
	for _, value := range []string{`"59s"`, `"30s"`, `"0"`, `"-5m"`, `"soon"`, `""`, `360`, `true`, `["6m"]`, `{}`} {
		_, err := ParseConfigBytes(provimuxReturnYAML(value))
		if err == nil || !strings.Contains(err.Error(), "routing.session-affinity-return.cold-after") {
			t.Fatalf("%s: %v; want a refusal naming the key", value, err)
		}
	}
}

func TestProvimuxSessionReturnSettingAbsent(t *testing.T) {
	for _, raw := range []string{
		"config-version: 8\nrouting:\n  session-affinity: true\n",
		"config-version: 8\nrouting:\n  session-affinity: true\n  session-affinity-return: {}\n",
		"config-version: 8\nrouting:\n  session-affinity: true\n  session-affinity-return:\n",
		"config-version: 8\nrouting:\n  session-affinity: true\n  session-affinity-return:\n    cold-after: null\n", // null is off
	} {
		cfg, err := ParseConfigBytes([]byte(raw))
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if _, set := cfg.ProvimuxSessionReturnColdAfter(); set {
			t.Fatalf("%q: set", raw)
		}
	}
	var none *Config
	if _, set := none.ProvimuxSessionReturnColdAfter(); set {
		t.Fatal("nil config: set")
	}
	built := &Config{}
	built.Routing.SessionAffinityReturn = &ProvimuxSessionReturn{ColdAfter: "30s"} // built in code, never read
	if _, set := built.ProvimuxSessionReturnColdAfter(); set {
		t.Fatal("a value under a minute built in code is set")
	}
}
