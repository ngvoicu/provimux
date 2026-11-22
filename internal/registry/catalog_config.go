package registry

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// CatalogSources overrides the source of each model catalog independently.
// Empty values retain the process default (embedded or official remote catalogs).
type CatalogSources struct {
	Catalog      string `yaml:"catalog" json:"catalog,omitempty"`
	CodexCatalog string `yaml:"codex-catalog" json:"codex-catalog,omitempty"`
	DevinCatalog string `yaml:"devin-catalog" json:"devin-catalog,omitempty"`
	// LiveLists gives each login whose vendor publishes a model list (Claude, Codex, Meta, Devin,
	// Kimi, xAI, and Gemini API keys) the models of that list, with the vendor's values, and adds
	// the models the catalog doesn't have yet. On unless set to false (Provimux).
	LiveLists *bool `yaml:"live-lists,omitempty" json:"live-lists,omitempty"`
}

// provimuxLiveListsDefault is the value of an unset models.live-lists: on, except in a test binary.
// Upstream's tests build their configs without the key and assume the catalog unchanged, as they
// assume upstream's catalog files (model-lists.md 6.4); Provimux's own tests set the key or flip
// this value with SetProvimuxLiveListsDefault.
var provimuxLiveListsDefault atomic.Bool

func init() { provimuxLiveListsDefault.Store(!testing.Testing()) }

// SetProvimuxLiveListsDefault sets the value of an unset models.live-lists and returns the previous
// one. For tests.
func SetProvimuxLiveListsDefault(on bool) bool { return provimuxLiveListsDefault.Swap(on) }

// LiveListsOn reports whether live model lists are on. Unset means on (outside test binaries).
func (m CatalogSources) LiveListsOn() bool {
	if m.LiveLists == nil {
		return provimuxLiveListsDefault.Load()
	}
	return *m.LiveLists
}

// Validate rejects relative paths and unsupported URL schemes.
func (m CatalogSources) Validate() error {
	for name, source := range map[string]string{"catalog": m.Catalog, "codex-catalog": m.CodexCatalog, "devin-catalog": m.DevinCatalog} {
		if source == "" || filepath.IsAbs(source) {
			continue
		}
		parsed, errParse := url.Parse(source)
		if errParse != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return fmt.Errorf("models.%s must be an http(s) URL or an absolute local path", name)
		}
	}
	return nil
}
