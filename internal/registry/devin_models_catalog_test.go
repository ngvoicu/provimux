package registry

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
)

// devinOutputLimitPlaceholder is the max_completion_tokens upstream's
// fetch_devin_models writes for every Devin model, whatever Devin says.
const devinOutputLimitPlaceholder = 64000

// devinOutputLimitReally64000 lists the catalog models whose Devin model config
// itself says 64000 (read with go run ./cmd/fetch_devin_models). None as of
// 2026-11-26: every variant Devin serves reports its own limit.
var devinOutputLimitReally64000 = map[string]bool{}

// devinCatalogStrictEnv makes TestEmbeddedDevinCatalogHasNoOutputLimitPlaceholder
// fail instead of skip (SYNC.md, "Devin models at the placeholder output limit").
const devinCatalogStrictEnv = "PMX_DEVIN_CATALOG_STRICT"

// provimuxWarningPrefix starts a test output line that the sync check
// (check.sh verdict) copies into the sync issue: a warning that does not fail
// the check.
const provimuxWarningPrefix = "provimux-warning: "

// devinModelsAtPlaceholder returns, sorted, the ids of the Devin models in a
// devin_models.json whose output limit is upstream's placeholder (or none) and
// not listed in devinOutputLimitReally64000.
func devinModelsAtPlaceholder(t *testing.T, data []byte) []string {
	t.Helper()
	var catalog struct {
		Devin []struct {
			ID                  string `json:"id"`
			MaxCompletionTokens int    `json:"max_completion_tokens"`
		} `json:"devin"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("parse devin_models.json: %v", err)
	}
	if len(catalog.Devin) == 0 {
		t.Fatal("devin_models.json has no Devin models")
	}
	var placeholder []string
	for _, m := range catalog.Devin {
		if devinOutputLimitReally64000[m.ID] {
			continue
		}
		if m.MaxCompletionTokens == devinOutputLimitPlaceholder || m.MaxCompletionTokens <= 0 {
			placeholder = append(placeholder, m.ID)
		}
	}
	sort.Strings(placeholder)
	return placeholder
}

// TestEmbeddedDevinCatalogHasNoOutputLimitPlaceholder names the Devin models in
// the embedded catalog that carry upstream's placeholder output limit (or none)
// instead of Devin's own: typically models upstream added after the list was
// last regenerated. It reports them and skips, so that a new upstream Devin
// model does not stop the automatic sync; the sync check shows the skip's line
// in the sync issue. With PMX_DEVIN_CATALOG_STRICT=1 it fails instead (for
// whoever regenerates internal/registry/models/devin_models.json with
// go run ./cmd/fetch_devin_models, which needs a Devin login).
func TestEmbeddedDevinCatalogHasNoOutputLimitPlaceholder(t *testing.T) {
	placeholder := devinModelsAtPlaceholder(t, embeddedDevinModelsJSON)
	if len(placeholder) == 0 {
		return
	}
	msg := devinPlaceholderMessage(placeholder)
	if os.Getenv(devinCatalogStrictEnv) == "1" {
		t.Fatal(msg)
	}
	t.Skip(provimuxWarningPrefix + msg + " (" + devinCatalogStrictEnv + "=1 makes this test fail)")
}

func devinPlaceholderMessage(ids []string) string {
	return fmt.Sprintf("%d Devin model(s) carry upstream's placeholder output limit %d (or none), not Devin's own: %s; "+
		"regenerate internal/registry/models/devin_models.json with go run ./cmd/fetch_devin_models",
		len(ids), devinOutputLimitPlaceholder, strings.Join(ids, ", "))
}

// TestDevinModelsAtPlaceholderNamesNewModels: a catalog with a model at the
// placeholder, one without a limit and one with its own limit names exactly the
// first two.
func TestDevinModelsAtPlaceholderNamesNewModels(t *testing.T) {
	data := []byte(`{"devin":[
		{"id":"provimux-test-devin-own","max_completion_tokens":128000},
		{"id":"provimux-test-devin-placeholder","max_completion_tokens":64000},
		{"id":"provimux-test-devin-none"}]}`)
	got := strings.Join(devinModelsAtPlaceholder(t, data), ",")
	if want := "provimux-test-devin-none,provimux-test-devin-placeholder"; got != want {
		t.Fatalf("devinModelsAtPlaceholder = %q, want %q", got, want)
	}
	if msg := devinPlaceholderMessage([]string{"a", "b"}); !strings.HasPrefix(msg, "2 Devin model(s) carry upstream's placeholder output limit 64000 (or none), not Devin's own: a, b;") {
		t.Fatalf("message = %q", msg)
	}
}
