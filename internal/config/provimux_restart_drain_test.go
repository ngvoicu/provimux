package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withProvimuxRestartDrainDefault gives the test the default a real gateway has (a test binary has 0).
func withProvimuxRestartDrainDefault(t *testing.T) {
	t.Helper()
	previous := provimuxRestartDrainDefault
	provimuxRestartDrainDefault = ProvimuxRestartDrainDefaultSeconds
	t.Cleanup(func() { provimuxRestartDrainDefault = previous })
}

// Provimux: the number upstream's stop is given. Provimux's key decides alone when set; unset, 25 or upstream's
// shutdown-timeout when that is larger.
func TestProvimuxStopDrainSeconds(t *testing.T) {
	withProvimuxRestartDrainDefault(t)
	cases := []struct {
		name string
		yaml string
		want int
	}{
		{"neither key", "config-version: 8\nserver:\n  port: 8317\n", 25},
		{"upstream 0 from the example", "config-version: 8\nserver:\n  shutdown-timeout: 0\n", 25},
		{"legacy layout, upstream 0", "shutdown-timeout: 0\n", 25},
		{"Provimux 0 over upstream 20", "config-version: 8\nserver:\n  shutdown-timeout: 20\n  restart-drain-seconds: 0\n", 0},
		{"Provimux 10 over upstream 20", "config-version: 8\nserver:\n  shutdown-timeout: 20\n  restart-drain-seconds: 10\n", 10},
		{"Provimux 30", "config-version: 8\nserver:\n  restart-drain-seconds: 30\n", 30},
		{"legacy layout, Provimux 9", "restart-drain-seconds: 9\n", 9},
		{"upstream 28, larger than 25", "config-version: 8\nserver:\n  shutdown-timeout: 28\n", 28},
		{"upstream 5, smaller than 25", "config-version: 8\nserver:\n  shutdown-timeout: 5\n", 25},
		{"upstream 60: upstream's own 30 s stop bounds it", "config-version: 8\nserver:\n  shutdown-timeout: 60\n", 60},
		{"explicit null is unset", "config-version: 8\nserver:\n  restart-drain-seconds: null\n  shutdown-timeout: 0\n", 25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfigBytes([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := cfg.ProvimuxStopDrainSeconds(); got != tc.want {
				t.Fatalf("budget = %d, want %d", got, tc.want)
			}
		})
	}
}

// Only a whole number from 0 to 30 is accepted, in the file and in a management write (both parse here).
func TestProvimuxRestartDrainRefusesOtherValues(t *testing.T) {
	for _, value := range []string{"-1", "31", "2.75", "30.0", `"10"`, "ten", "true", "[10]", "{a: 1}", "1e1"} {
		t.Run(value, func(t *testing.T) {
			_, err := ParseConfigBytes([]byte("config-version: 8\nserver:\n  restart-drain-seconds: " + value + "\n"))
			if err == nil || !strings.Contains(err.Error(), "restart-drain-seconds must be a whole number of seconds from 0 to 30") {
				t.Fatalf("err = %v", err)
			}
			if errV8 := ValidateV8Config([]byte("config-version: 8\nserver:\n  restart-drain-seconds: " + value + "\n")); errV8 == nil {
				t.Fatal("ValidateV8Config accepted it")
			}
		})
	}
	for _, value := range []string{"0", "1", "25", "30", "0x1e"} {
		if _, err := ParseConfigBytes([]byte("config-version: 8\nserver:\n  restart-drain-seconds: " + value + "\n")); err != nil {
			t.Fatalf("%s refused: %v", value, err)
		}
	}
}

// A test binary keeps upstream's behaviour for a config without the key: upstream's tests expect it.
func TestProvimuxRestartDrainDefaultInTestBinary(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("config-version: 8\nserver:\n  shutdown-timeout: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ProvimuxStopDrainSeconds(); got != 0 {
		t.Fatalf("budget in a test binary = %d, want 0", got)
	}
	cfg.ShutdownTimeoutSeconds = 7
	if got := cfg.ProvimuxStopDrainSeconds(); got != 7 {
		t.Fatalf("budget in a test binary = %d, want upstream's 7", got)
	}
}

// withoutServerID drops the server-id line upstream adds on first load.
func withoutServerID(text string) string {
	var kept []string
	for _, line := range strings.SplitAfter(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "server-id:") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "")
}

// Loading a config never writes the key (upstream's first load only adds its server-id), and a save of upstream's own (any management write other than the
// generic config path) keeps the key as the file has it: absent, 0 or 10.
func TestProvimuxRestartDrainSurvivesSaves(t *testing.T) {
	for _, tc := range []struct{ name, line, want string }{
		{"unset", "", ""},
		{"zero", "  restart-drain-seconds: 0\n", "restart-drain-seconds: 0"},
		{"ten", "  restart-drain-seconds: 10\n", "restart-drain-seconds: 10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			original := "config-version: 8\nserver:\n  port: 8317\n  # upstream's comment\n  shutdown-timeout: 0\n" + tc.line
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfigOptional(path, false)
			if err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(path); withoutServerID(string(data)) != original {
				t.Fatalf("loading rewrote the file:\n%s", data)
			}
			cfg.Debug = !cfg.Debug
			if err = SaveConfigPreserveComments(path, cfg); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			text := string(data)
			if tc.want == "" && strings.Contains(text, "restart-drain-seconds") || tc.want != "" && !strings.Contains(text, tc.want) {
				t.Fatalf("after a save:\n%s", text)
			}
			if !strings.Contains(text, "shutdown-timeout: 0") || !strings.Contains(text, "# upstream's comment") {
				t.Fatalf("upstream's key or comment changed:\n%s", text)
			}
			again, err := LoadConfigOptional(path, false)
			if err != nil {
				t.Fatal(err)
			}
			if (again.RestartDrainSeconds == nil) != (cfg.RestartDrainSeconds == nil) ||
				again.RestartDrainSeconds != nil && *again.RestartDrainSeconds != *cfg.RestartDrainSeconds {
				t.Fatalf("value after save = %v, before = %v", again.RestartDrainSeconds, cfg.RestartDrainSeconds)
			}
		})
	}
}
