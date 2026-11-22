package cliproxy

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the Kimi login's live model list. Kimi's own CLI lists the account's models with
// GET <base>/v1/models (MoonshotAI/kimi-cli, src/kimi_cli/auth/platforms.py) and reads id,
// display_name, context_length, supports_reasoning, supports_image_in and supports_video_in. The
// answer has no output limit and no effort levels (only whether the model reasons), so those stay
// the catalog's, and a model the catalog lacks gets none.

type liveModelListGet func(rawURL string, header http.Header) ([]byte, error)

// kimiLiveModelListURL is the models path on the base the login's chat requests use.
func kimiLiveModelListURL(auth *coreauth.Auth) string {
	base := strings.TrimRight(helps.ResolveKimiBaseURL(auth), "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/models"
	}
	return base + "/v1/models"
}

func fetchKimiLiveModelList(auth *coreauth.Auth, get liveModelListGet) (liveModelList, error) {
	body, err := get(kimiLiveModelListURL(auth), nil)
	if err != nil {
		return liveModelList{}, err
	}
	return parseKimiLiveModelList(body, liveModelListNow())
}

// parseKimiLiveModelList reads {"data":[...]}. A page that says it has more is incomplete.
func parseKimiLiveModelList(body []byte, now time.Time) (liveModelList, error) {
	if !ojson.ValidBytes(body) {
		return liveModelList{}, fmt.Errorf("kimi model list: invalid JSON")
	}
	root := ojson.ParseBytes(body)
	if root.Get("has_more").Bool() {
		return liveModelList{}, fmt.Errorf("kimi model list: %w", errLiveModelListIncomplete)
	}
	if !root.Get("data").IsArray() {
		return liveModelList{}, fmt.Errorf("kimi model list: %w", errLiveModelListMalformed)
	}
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for _, item := range root.Get("data").Array() {
		id := strings.TrimSpace(item.Get("id").String())
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, dup := list.ids[key]; dup {
			continue
		}
		list.ids[key] = struct{}{}
		model := &ModelInfo{ID: id, Object: "model", OwnedBy: "moonshot", Type: "kimi",
			DisplayName: strings.TrimSpace(item.Get("display_name").String()), ContextLength: int(item.Get("context_length").Int()),
			SupportedOutputModalities: []string{"text"}}
		if model.DisplayName == "" {
			model.DisplayName = id
		}
		if created := item.Get("created").Int(); created > 0 {
			model.Created = created
		}
		// Only a true flag counts: Kimi's own CLI adds image and video input for kimi-k2 models
		// whatever the flags say, so a false flag is not taken as a statement.
		if image, video := item.Get("supports_image_in").Bool(), item.Get("supports_video_in").Bool(); image || video {
			model.SupportedInputModalities = []string{"text"}
			if image {
				model.SupportedInputModalities = append(model.SupportedInputModalities, "image")
			}
			if video {
				model.SupportedInputModalities = append(model.SupportedInputModalities, "video")
			}
		}
		list.extra = append(list.extra, model)
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("kimi")
	}
	return list, nil
}
