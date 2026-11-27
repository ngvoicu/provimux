package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

// Provimux-only: GET /v8/management/provimux/restart-drain. How long the next stop lets the answers in flight
// finish (server.restart-drain-seconds), and where that number comes from, so the console does not keep its own
// copy of the rule (.github/provimux/design/restart-drain-state.md). Read-only; the setting itself is read and
// written at /v8/management/config/server/restart-drain-seconds.

// ProvimuxRestartDrainState is the endpoint's answer.
type ProvimuxRestartDrainState struct {
	// Seconds is the budget a stop now is given: the configuration the running process holds (after its hot
	// reloads; a reload it refused left the one before), the one upstream's stop reads.
	Seconds int `json:"seconds"`
	// WaitsAtMost is Seconds within upstream's bound on the whole stop (30 seconds).
	WaitsAtMost int `json:"waits_at_most"`
	// Set reports whether the configuration file sets server.restart-drain-seconds (null does not set it).
	Set bool `json:"set"`
	// Written is the value as the file writes it, when set (YAML text: a quoted value keeps its quotes).
	Written *string `json:"written,omitempty"`
	// WrittenAccepted, when set: whether the gateway accepts Written. False: a reload of this file is refused (the
	// running process keeps Seconds) and a start with it fails at loading the configuration.
	WrittenAccepted *bool `json:"written_accepted,omitempty"`
	// WrittenSeconds is what Written gives; absent when the gateway refuses it (not a whole number).
	WrittenSeconds *int `json:"written_seconds,omitempty"`
	// WrittenClamped: Written is a whole number outside Min..Max, used as the nearest bound.
	WrittenClamped bool `json:"written_clamped,omitempty"`
	// WrittenError says why Written is refused.
	WrittenError string `json:"written_error,omitempty"`
	// Default is the budget when the setting is not set: the default, or server.shutdown-timeout when larger.
	Default int `json:"default"`
	// ShutdownTimeout is server.shutdown-timeout as the gateway runs with it.
	ShutdownTimeout int `json:"shutdown_timeout"`
	Min             int `json:"min"`
	Max             int `json:"max"`
	// StopLimit is upstream's bound on the whole stop, in seconds.
	StopLimit int `json:"stop_limit"`
	// FileError says why the configuration file could not be read (Set and Written are then unknown).
	FileError string `json:"file_error,omitempty"`
}

// provimuxStopLimitSeconds is upstream's bound on the whole stop (Service.Run's shutdown context).
const provimuxStopLimitSeconds = 30

// ProvimuxRestartDrain answers the state of server.restart-drain-seconds, the wait from the handler's configuration.
func (h *Handler) ProvimuxRestartDrain(c *gin.Context) {
	h.ProvimuxRestartDrainOf(c, h.configSnapshot())
}

// ProvimuxRestartDrainOf answers the state with running as the configuration a stop now reads (the server's).
func (h *Handler) ProvimuxRestartDrainOf(c *gin.Context, running *config.Config) {
	cfg := running
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not available"})
		return
	}
	state := ProvimuxRestartDrainState{
		Seconds:         cfg.ProvimuxStopDrainSeconds(),
		Default:         cfg.ProvimuxStopDrainDefaultSeconds(),
		ShutdownTimeout: cfg.ShutdownTimeoutSeconds,
		Min:             0,
		Max:             config.ProvimuxRestartDrainMaxSeconds,
		StopLimit:       provimuxStopLimitSeconds,
	}
	state.WaitsAtMost = min(state.Seconds, state.StopLimit)
	if h.configFilePath == "" {
		state.FileError = "no configuration file"
	} else if data, errRead := config.ReadConfigFile(h.configFilePath); errRead != nil {
		state.FileError = errRead.Error()
	} else if node, errFind := provimuxRestartDrainNode(data); errFind != nil {
		state.FileError = errFind.Error()
	} else if node != nil {
		state.Set = true
		written := provimuxWritten(node)
		state.Written = &written
		used, clamped, errValue := config.ProvimuxRestartDrainRead(node)
		accepted := errValue == nil
		state.WrittenAccepted = &accepted
		if !accepted {
			state.WrittenError = errValue.Error()
		} else {
			state.WrittenSeconds, state.WrittenClamped = &used, clamped
		}
	}
	c.JSON(http.StatusOK, state)
}

// provimuxRestartDrainNode finds server.restart-drain-seconds in the file (or restart-drain-seconds at the top, the
// layout before v8): nil when absent or null.
func provimuxRestartDrainNode(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	find := func(mapping *yaml.Node, key string) *yaml.Node {
		if mapping == nil || mapping.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				return mapping.Content[i+1]
			}
		}
		return nil
	}
	node := find(find(root, "server"), "restart-drain-seconds")
	if node == nil {
		node = find(root, "restart-drain-seconds")
	}
	if node == nil || (node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null") {
		return nil, nil
	}
	return node, nil
}

// provimuxWritten is a value as the file writes it, as YAML on one line: a quoted scalar keeps its quotes, so "10"
// (refused) never reads like 10.
func provimuxWritten(node *yaml.Node) string {
	if node.Kind == yaml.ScalarNode && node.Style == 0 {
		return node.Value
	}
	out, errMarshal := yaml.Marshal(node)
	if errMarshal != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\n", " "))
}
