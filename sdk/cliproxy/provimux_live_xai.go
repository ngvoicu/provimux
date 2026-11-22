package cliproxy

import (
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the xAI login's live model list, from the API host the login already uses for its
// websocket, compact and speech requests (api.x.ai, or the login's own base URL when it is not
// the Grok CLI chat proxy). Two requests (docs.x.ai, REST API reference, "Models"):
//   - GET /v1/language-models: the chat models the credential may use, with input and output
//     modalities, aliases and capabilities.reasoning_effort;
//   - GET /v1/models: context_length per id.
//
// Both must succeed: a fetch with one answer only is a failed fetch. The answers have no output
// limit. That api.x.ai answers these paths for a login's token (scope api:access) is not verified
// (model-lists.md section 12).

// xaiLiveModelListBase is the base of the login's API requests (xaiCompactBaseURL in the executor).
func xaiLiveModelListBase(auth *coreauth.Auth) string {
	base := strings.TrimSpace(auth.Attributes["base_url"])
	if base == "" {
		base, _ = auth.Metadata["base_url"].(string)
		base = strings.TrimSpace(base)
	}
	normalized := strings.TrimRight(base, "/")
	if normalized == "" || strings.EqualFold(normalized, strings.TrimRight(xaiauth.CLIChatProxyBaseURL, "/")) {
		normalized = strings.TrimRight(xaiauth.DefaultAPIBaseURL, "/")
	}
	return normalized
}

func fetchXAILiveModelList(auth *coreauth.Auth, get liveModelListGet) (liveModelList, error) {
	base := xaiLiveModelListBase(auth)
	languageModels, err := get(base+"/language-models", nil)
	if err != nil {
		return liveModelList{}, err
	}
	models, err := get(base+"/models", nil)
	if err != nil {
		return liveModelList{}, err
	}
	return parseXAILiveModelList(languageModels, models, liveModelListNow())
}

// parseXAILiveModelList joins /v1/language-models (which models, and their fields) with
// /v1/models (their context windows).
func parseXAILiveModelList(languageModels, models []byte, now time.Time) (liveModelList, error) {
	if !ojson.ValidBytes(languageModels) || !ojson.ValidBytes(models) {
		return liveModelList{}, fmt.Errorf("xai model list: invalid JSON")
	}
	languageRoot := ojson.ParseBytes(languageModels)
	modelsRoot := ojson.ParseBytes(models)
	if !languageRoot.Get("models").IsArray() || !modelsRoot.Get("data").IsArray() {
		return liveModelList{}, fmt.Errorf("xai model list: %w", errLiveModelListMalformed)
	}
	windows := map[string]int{}
	for _, item := range modelsRoot.Get("data").Array() {
		if id := strings.ToLower(strings.TrimSpace(item.Get("id").String())); id != "" {
			windows[id] = int(item.Get("context_length").Int())
		}
	}
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for _, item := range languageRoot.Get("models").Array() {
		id := strings.TrimSpace(item.Get("id").String())
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, dup := list.ids[key]; dup {
			continue
		}
		list.ids[key] = struct{}{}
		model := &ModelInfo{ID: id, Object: "model", OwnedBy: "xai", Type: "xai", DisplayName: id, Name: id,
			Created: item.Get("created").Int(), ContextLength: windows[key],
			SupportedInputModalities:  xaiLiveModalities(item.Get("input_modalities")),
			SupportedOutputModalities: xaiLiveModalities(item.Get("output_modalities"))}
		efforts := item.Get("capabilities.reasoning_effort").Array()
		levels := liveModelListLevels(func(level string) bool {
			for _, effort := range efforts {
				if strings.EqualFold(effort.String(), level) {
					return true
				}
			}
			return false
		})
		if len(levels) > 0 {
			model.Thinking = &registry.ThinkingSupport{Levels: levels}
			for _, effort := range efforts {
				if strings.EqualFold(effort.String(), "none") {
					model.Thinking.ZeroAllowed = true
				}
			}
			// The effort list says whether "none" (off) is accepted: a stated value either way.
			list.noteStated(id, liveStatedZeroAllowed)
		}
		list.extra = append(list.extra, model)
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("xai")
	}
	return list, nil
}

func xaiLiveModalities(values ojson.Result) []string {
	var out []string
	for _, value := range values.Array() {
		if modality := strings.ToLower(strings.TrimSpace(value.String())); modality != "" {
			out = append(out, modality)
		}
	}
	return out
}
