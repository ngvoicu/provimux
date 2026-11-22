package cliproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protowire"
)

// Provimux: the Devin login's live model list. Devin's server answers
// ApiServerService/GetCliModelConfigs (Connect, unary, protobuf) with one entry per variant: the id
// (field 22), whose suffix names the effort (-low, -high, -max-fast, ...), a label (1), a
// multimodal flag (5), a vendor (10), a context window (18) and, inside the model config (23), an
// output limit (13). The request and the field numbers are upstream's own tool's
// (cmd/fetch_devin_models); the registry's own validator joins the variants into one model per
// base with its effort levels, as for upstream's devin_models.json. No output limit is assumed
// where the answer states none.

const devinLiveModelListPath = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"

// devinLiveModelListDo sends the request; tests replace it.
var devinLiveModelListDo = func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return helps.NewDevinHTTPClient(ctx, cfg, auth, 0).Do(req)
}

// devinLiveCredentials reads the session token, server and device seed the way the Devin executor
// does (devinAuthCredentials), without importing it.
func devinLiveCredentials(auth *coreauth.Auth) (token, baseURL, deviceSeed string) {
	baseURL = helps.DevinDefaultBaseURL
	if auth == nil {
		return "", baseURL, ""
	}
	for _, key := range []string{"api_key", "session_token", "token"} {
		if token == "" {
			token = strings.TrimSpace(auth.Attributes[key])
		}
	}
	if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
		baseURL = value
	}
	deviceSeed = strings.TrimSpace(auth.Attributes["device_seed"])
	for _, key := range []string{"api_key", "session_token", "token"} {
		if value, _ := auth.Metadata[key].(string); token == "" && strings.TrimSpace(value) != "" {
			token = strings.TrimSpace(value)
		}
	}
	if value, _ := auth.Metadata["base_url"].(string); strings.TrimSpace(value) != "" && baseURL == helps.DevinDefaultBaseURL {
		baseURL = strings.TrimSpace(value)
	}
	if value, _ := auth.Metadata["device_seed"].(string); deviceSeed == "" && strings.TrimSpace(value) != "" {
		deviceSeed = strings.TrimSpace(value)
	}
	return token, strings.TrimRight(baseURL, "/"), deviceSeed
}

func fetchDevinLiveModelList(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (liveModelList, error) {
	token, baseURL, deviceSeed := devinLiveCredentials(auth)
	if token == "" {
		return liveModelList{}, fmt.Errorf("prepare credentials: devin login has no session token")
	}
	body := protowire.AppendTag(nil, 1, protowire.BytesType)
	body = protowire.AppendBytes(body, helps.BuildDevinClientMetadataBytes(token, deviceSeed, ""))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+devinLiveModelListPath, bytes.NewReader(body))
	if err != nil {
		return liveModelList{}, err
	}
	req.Header.Set("Authorization", "Basic "+token+"-"+token)
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Accept", "*/*")
	req.Header["User-Agent"] = []string{""}
	resp, err := devinLiveModelListDo(ctx, cfg, auth, req)
	if err != nil {
		return liveModelList{}, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("live model list: close devin response body: %v", errClose)
		}
	}()
	data, err := io.ReadAll(io.LimitReader(resp.Body, liveModelListMaxBody+1))
	if err != nil {
		return liveModelList{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return liveModelList{}, fmt.Errorf("HTTP %d from %s", resp.StatusCode, devinLiveModelListPath)
	}
	if len(data) > liveModelListMaxBody {
		return liveModelList{}, fmt.Errorf("devin model list: %w", errLiveModelListTooLarge)
	}
	return parseDevinLiveModelList(data, liveModelListNow())
}

// devinLiveVariant is one entry of GetCliModelConfigs.
type devinLiveVariant struct {
	ID         string `json:"id"`
	Label      string
	Context    int
	MaxOutput  int
	Multimodal bool
	Vendor     uint64
}

// parseDevinLiveModelList reads the protobuf answer. A malformed message (a cut-off answer
// included) fails as a whole: a partial list is never published.
func parseDevinLiveModelList(body []byte, now time.Time) (liveModelList, error) {
	var variants []devinLiveVariant
	seen := map[string]struct{}{}
	for rest := body; len(rest) > 0; {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return liveModelList{}, fmt.Errorf("devin model list: %w", errLiveModelListMalformed)
		}
		rest = rest[n:]
		if num == 1 && typ == protowire.BytesType {
			message, m := protowire.ConsumeBytes(rest)
			if m < 0 {
				return liveModelList{}, fmt.Errorf("devin model list: %w", errLiveModelListMalformed)
			}
			rest = rest[m:]
			variant, errVariant := parseDevinLiveVariant(message)
			if errVariant != nil {
				return liveModelList{}, fmt.Errorf("devin model list: %w", errVariant)
			}
			key := strings.ToLower(variant.ID)
			if variant.ID == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			variants = append(variants, variant)
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, rest)
		if m < 0 {
			return liveModelList{}, fmt.Errorf("devin model list: %w", errLiveModelListMalformed)
		}
		rest = rest[m:]
	}
	if len(variants) == 0 {
		return liveModelList{}, errEmptyLiveModelList("devin")
	}
	return devinLiveModelListOf(variants, now)
}

func parseDevinLiveVariant(b []byte) (devinLiveVariant, error) {
	var variant devinLiveVariant
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return variant, errLiveModelListMalformed
		}
		b = b[n:]
		switch {
		case typ == protowire.BytesType && (num == 1 || num == 22):
			value, m := protowire.ConsumeString(b)
			if m < 0 {
				return variant, errLiveModelListMalformed
			}
			b = b[m:]
			if num == 1 {
				variant.Label = strings.TrimSpace(value)
			} else {
				variant.ID = strings.TrimSpace(value)
			}
			continue
		case typ == protowire.VarintType && (num == 5 || num == 10 || num == 18):
			value, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return variant, errLiveModelListMalformed
			}
			b = b[m:]
			switch num {
			case 5:
				variant.Multimodal = value == 1
			case 10:
				variant.Vendor = value
			case 18:
				variant.Context = int(value)
			}
			continue
		case typ == protowire.BytesType && num == 23:
			config, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return variant, errLiveModelListMalformed
			}
			b = b[m:]
			variant.MaxOutput = devinLiveMaxOutput(config)
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return variant, errLiveModelListMalformed
		}
		b = b[m:]
	}
	return variant, nil
}

// devinLiveMaxOutput reads field 13 of the model config; 0 when absent or malformed.
func devinLiveMaxOutput(b []byte) int {
	maxOutput := 0
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return 0
		}
		b = b[n:]
		if num == 13 && typ == protowire.VarintType {
			value, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return 0
			}
			maxOutput = int(value)
			b = b[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return 0
		}
		b = b[m:]
	}
	return maxOutput
}

// devinLiveVendor names the owner as upstream's tool does.
func devinLiveVendor(vendor uint64, id string) string {
	switch vendor {
	case 1:
		return "cognition"
	case 2:
		return "openai"
	case 3:
		return "anthropic"
	case 4:
		return "google"
	case 6:
		return "deepseek"
	case 7:
		return "moonshot"
	case 9:
		return "zhipu"
	case 11:
		return "nvidia"
	}
	if strings.Contains(strings.ToLower(id), "grok") {
		return "xai"
	}
	return "devin"
}

// devinLiveModelListOf joins the variants with the registry's own Devin validator, which
// namespaces the ids (devin/<base>) and gathers the effort levels.
func devinLiveModelListOf(variants []devinLiveVariant, now time.Time) (liveModelList, error) {
	raw := make([]map[string]any, 0, len(variants))
	for _, variant := range variants {
		modalities := []string{"text"}
		if variant.Multimodal {
			modalities = append(modalities, "image")
		}
		entry := map[string]any{"id": variant.ID, "object": "model", "type": "devin",
			"owned_by": devinLiveVendor(variant.Vendor, variant.ID), "display_name": variant.Label,
			"supportedInputModalities": modalities, "supportedOutputModalities": []string{"text"}}
		if variant.Label == "" {
			entry["display_name"] = variant.ID
		}
		if variant.Context > 0 {
			entry["context_length"] = variant.Context
		}
		if variant.MaxOutput > 0 {
			entry["max_completion_tokens"] = variant.MaxOutput
		}
		raw = append(raw, entry)
	}
	data, err := json.Marshal(map[string]any{"devin": raw})
	if err != nil {
		return liveModelList{}, err
	}
	models, err := registry.ValidateDevinModelsJSON(data)
	if err != nil {
		return liveModelList{}, fmt.Errorf("devin model list: %w", errors.Join(errLiveModelListMalformed, err))
	}
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for _, model := range models {
		if model == nil || model.ID == "" {
			continue
		}
		list.ids[strings.ToLower(model.ID)] = struct{}{}
		list.extra = append(list.extra, model)
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("devin")
	}
	return list, nil
}
