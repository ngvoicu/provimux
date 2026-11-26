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
	// PublishedLists asks providers.dev (one request, the same for every user, every 6 hours) for the
	// model lists of Provimux's login types; their values fill the fields a vendor's list leaves out
	// for a model the catalog lacks. On unless set to false (Provimux, model-lists.md step P3).
	PublishedLists *bool `yaml:"published-lists,omitempty" json:"published-lists,omitempty"`
	// AnsweredBy names, on each answer, the account that gave it by an opaque marker, and gives the account lists
	// the marker. On unless set to false; off in a test binary unless a test turns it on (Provimux, answered-by.md 6).
	AnsweredBy *bool `yaml:"answered-by,omitempty" json:"answered-by,omitempty"`
	// OnlyMyNames serves only the names the owner set: the aliases on his keys and logins, plus the
	// names under keep. Every other name is treated as excluded. Off when absent (Provimux,
	// only-my-names.md).
	OnlyMyNames *OnlyMyNames `yaml:"only-my-names,omitempty" json:"only-my-names,omitempty"`
}

// provimuxLiveListsDefault is the value of an unset models.live-lists: on, except in a test binary.
// Upstream's tests build their configs without the key and assume the catalog unchanged, as they
// assume upstream's catalog files (model-lists.md 6.4); Provimux's own tests set the key or flip
// this value with SetProvimuxLiveListsDefault.
var provimuxLiveListsDefault atomic.Bool

// provimuxPublishedListsDefault is the value of an unset models.published-lists: on, except in a test
// binary, where no test may reach providers.dev by accident.
var provimuxPublishedListsDefault atomic.Bool

func init() {
	provimuxLiveListsDefault.Store(!testing.Testing())
	provimuxPublishedListsDefault.Store(!testing.Testing())
}

// SetProvimuxPublishedListsDefault sets the value of an unset models.published-lists and returns the
// previous one. For tests.
func SetProvimuxPublishedListsDefault(on bool) bool { return provimuxPublishedListsDefault.Swap(on) }

// PublishedListsOn reports whether providers.dev's lists are fetched and used. Unset means on (outside
// test binaries).
func (m CatalogSources) PublishedListsOn() bool {
	if m.PublishedLists == nil {
		return provimuxPublishedListsDefault.Load()
	}
	return *m.PublishedLists
}

// AnsweredByOn reports whether answers and account lists name the account (models.answered-by). Unset means on,
// except in a test binary, where every existing test of the headers and the lists reads what it read.
func (m CatalogSources) AnsweredByOn() bool {
	if m.AnsweredBy == nil {
		return !testing.Testing()
	}
	return *m.AnsweredBy
}

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
