package config

import (
	"fmt"
	"sync"
	"testing"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// Provimux: the number of seconds upstream's own stop (api.Server.Stop) lets HTTP requests in flight finish
// before it closes them, read once per stop. Upstream reads server.shutdown-timeout, whose 0 (the value every
// install copied from the example) closes at once. Provimux only chooses the number; the stop itself is
// upstream's.

// ProvimuxRestartDrainDefaultSeconds is the budget when server.restart-drain-seconds is not set.
const ProvimuxRestartDrainDefaultSeconds = 25

// ProvimuxRestartDrainMaxSeconds is the largest value server.restart-drain-seconds takes. Upstream bounds the
// whole stop at 30 seconds anyway.
const ProvimuxRestartDrainMaxSeconds = 30

// provimuxRestartDrainDefault is the default in this process: 0 inside a test binary, so upstream's tests,
// which build configs without the key, keep upstream's immediate close (as models.live-lists does).
var provimuxRestartDrainDefault = ProvimuxRestartDrainDefaultSeconds

func init() {
	if testing.Testing() {
		provimuxRestartDrainDefault = 0
	}
}

// ProvimuxRestartDrain is server.restart-drain-seconds: a whole number of seconds from 0 to
// ProvimuxRestartDrainMaxSeconds.
type ProvimuxRestartDrain int

// UnmarshalYAML accepts a YAML integer. One above ProvimuxRestartDrainMaxSeconds is used as the maximum and one
// below 0 as 0, with a warning, so that a hand-typed number never keeps the gateway from starting (upstream's
// own keys start with any number too). Anything else (2.75, "10", true) is refused, as upstream refuses a wrong
// type. A management write of a number out of range is refused separately (provimuxRestartDrainWriteCheck).
func (d *ProvimuxRestartDrain) UnmarshalYAML(node *yaml.Node) error {
	var seconds int
	if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!int" || node.Decode(&seconds) != nil {
		return fmt.Errorf("server.restart-drain-seconds must be a whole number of seconds from 0 to %d, got %q",
			ProvimuxRestartDrainMaxSeconds, node.Value)
	}
	used := min(max(seconds, 0), ProvimuxRestartDrainMaxSeconds)
	provimuxWarnRestartDrain(node.Value, used, used != seconds)
	*d = ProvimuxRestartDrain(used)
	return nil
}

// The config is parsed several times at start and at each reload: the warning is written once per value found.
var provimuxRestartDrainWarned struct {
	sync.Mutex
	value string
}

func provimuxWarnRestartDrain(found string, used int, clamped bool) {
	provimuxRestartDrainWarned.Lock()
	defer provimuxRestartDrainWarned.Unlock()
	if !clamped {
		provimuxRestartDrainWarned.value = ""
		return
	}
	if provimuxRestartDrainWarned.value == found {
		return
	}
	provimuxRestartDrainWarned.value = found
	log.Warnf("server.restart-drain-seconds is %s; using %d (it takes 0 to %d)", found, used, ProvimuxRestartDrainMaxSeconds)
}

// provimuxRestartDrainWriteCheck is called by ValidateV8Config, which only the management API's config writes
// call: a number out of range is refused there, so the console never saves one. err is the error ValidateV8Config
// already has; it is returned first.
func provimuxRestartDrainWriteCheck(flat *yaml.Node, err error) error {
	if err != nil {
		return err
	}
	node := yamlPath(flat, "restart-drain-seconds")
	var seconds int
	if node == nil || node.Kind != yaml.ScalarNode || node.ShortTag() != "!!int" || node.Decode(&seconds) != nil {
		return nil // unset, null, or a wrong type the decode refuses
	}
	if seconds < 0 || seconds > ProvimuxRestartDrainMaxSeconds {
		return fmt.Errorf("server.restart-drain-seconds must be a whole number of seconds from 0 to %d, got %q",
			ProvimuxRestartDrainMaxSeconds, node.Value)
	}
	return nil
}

// ProvimuxStopDrainSeconds is the budget handed to upstream's stop: server.restart-drain-seconds when it is
// set (0 included: close at once); otherwise the default, or server.shutdown-timeout when that is larger.
func (cfg *Config) ProvimuxStopDrainSeconds() int {
	if cfg.RestartDrainSeconds != nil {
		return int(*cfg.RestartDrainSeconds)
	}
	return max(provimuxRestartDrainDefault, cfg.ShutdownTimeoutSeconds)
}

// ProvimuxStopDrain is ProvimuxStopDrainSeconds, read by upstream's stop, which logs at info level when it is going
// to wait, so that a stop with answers in flight does not look like a hang.
func (cfg *Config) ProvimuxStopDrain() int {
	seconds := cfg.ProvimuxStopDrainSeconds()
	if seconds > 0 {
		log.Infof("stopping: waiting up to %d seconds for the answers in flight", seconds)
	}
	return seconds
}
