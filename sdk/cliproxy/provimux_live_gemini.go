package cliproxy

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: a Gemini API key's live model list. GET /v1beta/models?pageSize=1000 on Google's host,
// following nextPageToken until there is none (ai.google.dev, "Models" API). Every page must
// arrive: a fetch that stops between pages is a failed fetch. Only models that answer
// generateContent are listed (embedding and other models are not chat models here). Each has
// name ("models/<id>"), displayName, description, inputTokenLimit, outputTokenLimit and
// supportedGenerationMethods; "thinking" is a yes or no only, so budgets and levels stay the
// catalog's, and a model the catalog lacks gets none. A key with its own base URL is never asked.

const (
	geminiLiveModelListHost     = "generativelanguage.googleapis.com"
	geminiLiveModelListPageSize = "1000"
	// geminiLiveModelListMaxPages bounds the pages of one fetch; more is a failed fetch.
	geminiLiveModelListMaxPages = 20
)

// liveModelListGeminiHost reports whether a Gemini key talks to Google's own host.
func liveModelListGeminiHost(auth *coreauth.Auth) bool {
	base := strings.TrimSpace(auth.Attributes["base_url"])
	if base == "" {
		return true
	}
	parsed, err := url.Parse(base)
	return err == nil && strings.EqualFold(parsed.Hostname(), geminiLiveModelListHost)
}

func fetchGeminiLiveModelList(_ *coreauth.Auth, get liveModelListGet) (liveModelList, error) {
	var pages [][]byte
	token := ""
	for page := 0; ; page++ {
		if page == geminiLiveModelListMaxPages {
			return liveModelList{}, fmt.Errorf("gemini model list: %w (more than %d pages)", errLiveModelListIncomplete, geminiLiveModelListMaxPages)
		}
		query := url.Values{"pageSize": {geminiLiveModelListPageSize}}
		if token != "" {
			query.Set("pageToken", token)
		}
		body, err := get("https://"+geminiLiveModelListHost+"/v1beta/models?"+query.Encode(), nil)
		if err != nil {
			return liveModelList{}, err
		}
		if !ojson.ValidBytes(body) {
			return liveModelList{}, fmt.Errorf("gemini model list: invalid JSON")
		}
		pages = append(pages, body)
		token = strings.TrimSpace(ojson.GetBytes(body, "nextPageToken").String())
		if token == "" {
			break
		}
	}
	return parseGeminiLiveModelList(pages, liveModelListNow())
}

// parseGeminiLiveModelList reads every page of one fetch. A page that still names a next page
// after the last one read means the fetch is incomplete.
func parseGeminiLiveModelList(pages [][]byte, now time.Time) (liveModelList, error) {
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for i, page := range pages {
		if !ojson.ValidBytes(page) {
			return liveModelList{}, fmt.Errorf("gemini model list: invalid JSON")
		}
		root := ojson.ParseBytes(page)
		if models := root.Get("models"); models.Exists() && !models.IsArray() {
			return liveModelList{}, fmt.Errorf("gemini model list: %w", errLiveModelListMalformed)
		}
		if i == len(pages)-1 && strings.TrimSpace(root.Get("nextPageToken").String()) != "" {
			return liveModelList{}, fmt.Errorf("gemini model list: %w", errLiveModelListIncomplete)
		}
		for _, item := range root.Get("models").Array() {
			name := strings.TrimSpace(item.Get("name").String())
			id := strings.TrimPrefix(name, "models/")
			if id == "" || !geminiLiveGenerates(item) {
				continue
			}
			key := strings.ToLower(id)
			if _, dup := list.ids[key]; dup {
				continue
			}
			list.ids[key] = struct{}{}
			model := &ModelInfo{ID: id, Name: name, Object: "model", OwnedBy: "google", Type: "gemini",
				DisplayName: strings.TrimSpace(item.Get("displayName").String()), Description: item.Get("description").String(),
				Version: item.Get("version").String(), InputTokenLimit: int(item.Get("inputTokenLimit").Int()),
				OutputTokenLimit: int(item.Get("outputTokenLimit").Int())}
			model.ContextLength, model.MaxCompletionTokens = model.InputTokenLimit, model.OutputTokenLimit
			if model.DisplayName == "" {
				model.DisplayName = id
			}
			for _, method := range item.Get("supportedGenerationMethods").Array() {
				if value := strings.TrimSpace(method.String()); value != "" {
					model.SupportedGenerationMethods = append(model.SupportedGenerationMethods, value)
				}
			}
			list.extra = append(list.extra, model)
		}
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("gemini")
	}
	return list, nil
}

func geminiLiveGenerates(item ojson.Result) bool {
	for _, method := range item.Get("supportedGenerationMethods").Array() {
		if method.String() == "generateContent" {
			return true
		}
	}
	return false
}
