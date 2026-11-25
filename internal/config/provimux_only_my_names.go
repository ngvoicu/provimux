package config

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
)

// Provimux: the rule of models.only-my-names from a config (only-my-names.md 2.2 and section 18). Rows 1, 2 and 4
// of the gateway's rule, and the model prefixes the config declares. An alias in a login's own file, and a prefix
// set only in a login's own file, are not the config's: a name or a copy they give is the owner's only when keep
// lists it in full. The rule is a function of the committed config alone.

// ProvimuxNamesRule builds the rule of models.only-my-names from cfg: the switch, Home mode, the alias of every
// API key's models row whose alias differs from its name (every key family and every openai-compatibility entry,
// disabled ones included), the alias of every oauth-model-alias row, keep, and the prefix of every key and
// provider entry (the copies of his names under those prefixes are his). It is built whether the switch is on or
// off: off, it is the preview.
func ProvimuxNamesRule(cfg *Config) *onlymynames.Rule {
	if cfg == nil {
		return nil
	}
	only := cfg.Models.OnlyMyNames
	rule := onlymynames.NewRule(only != nil && only.Enabled, cfg.Home.Enabled)
	rule.SetSource(cfg.SourceHash)
	for _, name := range provimuxAliasNames(cfg) {
		rule.AddName(name, onlymynames.SetByAlias)
	}
	if only != nil {
		rule.AddKeep(only.Keep)
	}
	for _, prefix := range provimuxPrefixes(cfg) {
		rule.AddPrefix(prefix)
	}
	return rule
}

// provimuxPrefixes lists the prefix of every entry the config synthesizes an account from (the families of
// internal/watcher/synthesizer/config.go, which trims it as AddPrefix does), disabled entries included.
func provimuxPrefixes(cfg *Config) []string {
	var out []string
	for _, entries := range [][]GeminiKey{cfg.GeminiKey, cfg.InteractionsKey} {
		for _, entry := range entries {
			out = append(out, entry.Prefix)
		}
	}
	for _, entry := range cfg.ClaudeKey {
		out = append(out, entry.Prefix)
	}
	for _, entries := range [][]CodexKey{cfg.CodexKey, cfg.XAIKey, cfg.MetaKey} {
		for _, entry := range entries {
			out = append(out, entry.Prefix)
		}
	}
	for _, entry := range cfg.VertexCompatAPIKey {
		out = append(out, entry.Prefix)
	}
	for _, entry := range cfg.OpenAICompatibility {
		out = append(out, entry.Prefix)
	}
	return out
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
