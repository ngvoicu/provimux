package synthesizer

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the two key settings of key-model-lists.md 6 on a config key's credential.
//
// fixed-models is read from the config at registration; the attribute is written only so that
// switching it changes the credential, and a config reload registers the key again (the watcher
// dispatches a config key only when its synthesized credential differs).

// provimuxFixedModelsAttr marks a key whose entry says fixed-models: true. Unset writes nothing,
// so an entry without the field synthesizes exactly as before.
func provimuxFixedModelsAttr(attrs map[string]string, fixed bool) {
	if fixed && attrs != nil {
		attrs["fixed_models"] = "true"
	}
}

// provimuxOpenAICompatAttrs gives an openai-compatibility key what a built-in key has: its entry's
// excluded-models (with their hash, so a changed list registers the key again) and fixed-models.
// The auth kind is left alone. An entry without either field adds nothing.
func provimuxOpenAICompatAttrs(a *coreauth.Auth, compat *config.OpenAICompatibility) {
	if a == nil || compat == nil {
		return
	}
	if len(compat.ExcludedModels) > 0 {
		ApplyAuthExcludedModelsMeta(a, compat.ExcludedModels, "")
	}
	provimuxFixedModelsAttr(a.Attributes, compat.FixedModels)
}
