package config

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Provimux: the names the owner set in the config, for models.only-my-names (only-my-names.md 2.2).
// Rows 1, 2 and 4 of the gateway's rule; row 3, an alias in a login's own file, is read on the model.

// ProvimuxNamesRule builds the rule of models.only-my-names from cfg: the switch, Home mode, the
// alias of every API key's models row whose alias differs from its name (every key family and every
// openai-compatibility entry, disabled ones included), the alias of every oauth-model-alias row, and
// keep. It is built whether the switch is on or off: off, it is the preview.
func ProvimuxNamesRule(cfg *Config) *registry.ProvimuxNamesRule {
	if cfg == nil {
		return nil
	}
	only := cfg.Models.OnlyMyNames
	rule := registry.NewProvimuxNamesRule(only != nil && only.Enabled, cfg.Home.Enabled)
	rule.SetSource(cfg.SourceHash)
	for _, name := range provimuxAliasNames(cfg) {
		rule.AddName(name, registry.ProvimuxNameSetByAlias)
	}
	if only != nil {
		rule.AddKeep(only.Keep)
	}
	return rule
}

// provimuxAliasNames lists the aliases of rows 1 and 2, in config order.
func provimuxAliasNames(cfg *Config) []string {
	var out []string
	keyRow := func(name, alias string) {
		name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
		if name != "" && alias != "" && alias != name {
			out = append(out, alias)
		}
	}
	for _, entries := range [][]GeminiKey{cfg.GeminiKey, cfg.InteractionsKey} {
		for _, entry := range entries {
			for _, model := range entry.Models {
				keyRow(model.Name, model.Alias)
			}
		}
	}
	for _, entry := range cfg.ClaudeKey {
		for _, model := range entry.Models {
			keyRow(model.Name, model.Alias)
		}
	}
	for _, entries := range [][]CodexKey{cfg.CodexKey, cfg.XAIKey, cfg.MetaKey} {
		for _, entry := range entries {
			for _, model := range entry.Models {
				keyRow(model.Name, model.Alias)
			}
		}
	}
	for _, entry := range cfg.VertexCompatAPIKey {
		for _, model := range entry.Models {
			keyRow(model.Name, model.Alias)
		}
	}
	for _, entry := range cfg.OpenAICompatibility {
		for _, model := range entry.Models {
			keyRow(model.Name, model.Alias)
		}
	}
	channels := make([]string, 0, len(cfg.OAuthModelAlias))
	for channel := range cfg.OAuthModelAlias {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	for _, channel := range channels {
		for _, row := range cfg.OAuthModelAlias[channel] {
			// Upstream skips a row whose alias equals its name when it applies aliases.
			keyRow(row.Name, row.Alias)
		}
	}
	return out
}
