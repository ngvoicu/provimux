package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Provimux: routing.session-affinity-return (.github/provimux/design/session-route.md 15.6). One key, cold-after: how
// long a conversation must be unused before its cache can be taken as cold. Off unless it is set; removing the key is
// "off". Only the setting lives here; the rule is sdk/cliproxy/auth's (provimux_session_return.go).

// ProvimuxSessionReturnMinColdAfter is the shortest cold-after the setting takes.
const ProvimuxSessionReturnMinColdAfter = time.Minute

// ProvimuxSessionReturn is routing.session-affinity-return.
type ProvimuxSessionReturn struct {
	// ColdAfter is a duration of at least one minute ("6m", "1h30m"). Not set: the rule is off.
	ColdAfter ProvimuxColdAfter `yaml:"cold-after,omitempty" json:"cold-after,omitempty"`
}

// ProvimuxColdAfter is routing.session-affinity-return.cold-after, kept as written. The duration it stands for is read
// with time.ParseDuration and only ever compared with another duration (finding 8 of the review: nothing is added to
// it and nothing turns it into seconds).
type ProvimuxColdAfter string

// provimuxColdAfterError names the key and what it takes.
func provimuxColdAfterError(got string) error {
	return fmt.Errorf("routing.session-affinity-return.cold-after must be a duration of at least one minute (\"6m\", \"1h30m\"), got %q", got)
}

// parseProvimuxColdAfter reads a written value: a duration of at least one minute.
func parseProvimuxColdAfter(value string) (time.Duration, error) {
	parsed, errParse := time.ParseDuration(strings.TrimSpace(value))
	if errParse != nil || parsed < ProvimuxSessionReturnMinColdAfter {
		return 0, provimuxColdAfterError(value)
	}
	return parsed, nil
}

// UnmarshalYAML accepts a duration string of at least one minute. Anything else (a number, a shorter or malformed
// duration, an empty string, a list) is refused where the configuration is read: the file at a start or a reload, a
// management write. null is off, as removing the key is (yaml.v3 leaves the value empty without calling this).
func (d *ProvimuxColdAfter) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.ScalarNode || node.ShortTag() != "!!str" {
		value := ""
		if node != nil {
			value = node.Value
		}
		return provimuxColdAfterError(value)
	}
	if _, errParse := parseProvimuxColdAfter(node.Value); errParse != nil {
		return errParse
	}
	*d = ProvimuxColdAfter(strings.TrimSpace(node.Value))
	return nil
}

// UnmarshalJSON holds a JSON copy of the configuration to the same rule.
func (d *ProvimuxColdAfter) UnmarshalJSON(data []byte) error {
	if string(data) == "null" { // null is off, as in YAML
		*d = ""
		return nil
	}
	var value string
	if errJSON := json.Unmarshal(data, &value); errJSON != nil {
		return provimuxColdAfterError(string(data))
	}
	if _, errParse := parseProvimuxColdAfter(value); errParse != nil {
		return errParse
	}
	*d = ProvimuxColdAfter(strings.TrimSpace(value))
	return nil
}

// ProvimuxSessionReturnColdAfter is cold-after as cfg sets it, and whether it sets it. A value that reached a Config
// without being read from YAML or JSON (built in code) is held to the same rule: one that is not a duration of at
// least a minute is not set.
func (cfg *Config) ProvimuxSessionReturnColdAfter() (time.Duration, bool) {
	if cfg == nil || cfg.Routing.SessionAffinityReturn == nil {
		return 0, false
	}
	value := strings.TrimSpace(string(cfg.Routing.SessionAffinityReturn.ColdAfter))
	if value == "" {
		return 0, false
	}
	parsed, errParse := parseProvimuxColdAfter(value)
	if errParse != nil {
		return 0, false
	}
	return parsed, true
}
