package savings

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func bookWith(list map[string]registry.ProvimuxOpenRouterPrice, table *priceTable) *priceBook {
	if table == nil {
		table = &priceTable{V: 1, byPair: map[string]tableEntry{}}
	}
	return &priceBook{openRouter: list, table: table}
}

func tableOf(t *testing.T, entries string) *priceTable {
	t.Helper()
	// date-safe: the table's date field, parsed and compared by a pure function; no clock reads it.
	table, err := parseTable([]byte(`{"v":1,"updated":"2026-11-20","models":[` + entries + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	return table
}

// Every row of the table in 02-prices.md section 4, against the list of 2026-11-20.
func TestResolveTheDesignTable(t *testing.T) {
	book := bookWith(testList(t), nil)
	for _, tc := range []struct{ provider, model, id, match string }{
		{"openrouter", "anthropic/claude-sonnet-5.5", "anthropic/claude-sonnet-5.5", matchExact},
		{"openrouter", "openai/gpt-5.6-sol:floor", "openai/gpt-5.6-sol", matchVariant},
		{"claude", "claude-opus-5-5", "anthropic/claude-opus-5.5", matchNormalized},
		{"claude", "claude-opus-4-5-20251101", "anthropic/claude-opus-4.5", matchNormalized},
		{"codex", "gpt-6.2-sol", "openai/gpt-6.2-sol", matchNormalized},
		{"antigravity", "claude-opus-5-5-high", "anthropic/claude-opus-5.5", matchNormalized},
		{"antigravity", "gemini-4-hydrogen-xhigh", "google/gemini-4-hydrogen", matchNormalized},
		{"devin", "gpt-5-6-sol", "openai/gpt-5.6-sol", matchNormalized},
		{"xai", "grok-4.7-build-fast", "", ""},
		{"openrouter", "openrouter/auto", "", ""},
		{"devin", "swe-2", "", ""},
	} {
		ref, p, ok := book.resolvePrice(tc.provider, tc.model)
		if tc.id == "" {
			if ok {
				t.Errorf("%s %s: want unpriced, got %+v", tc.provider, tc.model, ref)
			}
			continue
		}
		if !ok || ref.ID != tc.id || ref.Match != tc.match || ref.Source != sourceOpenRouter || p.Src != sourceOpenRouter {
			t.Errorf("%s %s: got %+v %v, want %s %s", tc.provider, tc.model, ref, ok, tc.id, tc.match)
		}
	}
}

func TestResolveStepsInOrder(t *testing.T) {
	list := map[string]registry.ProvimuxOpenRouterPrice{
		"anthropic/claude-opus-5.5": {Input: 4, Output: 20},
		"vendor/x":                  {Input: 1, Output: 2},
	}
	table := tableOf(t, `
		{"provider":"openrouter","model":"vendor/x","price":{"in":9,"out":9},"source":"https://example.com/a"},
		{"provider":"*","model":"vendor/x","price":{"in":7,"out":7},"source":"https://example.com/b"},
		{"provider":"*","model":"claude-opus-5-5","same_as":"anthropic/claude-opus-9","source":"https://example.com/c"}`)
	book := bookWith(list, table)
	ref, p, ok := book.resolvePrice("openrouter", "vendor/x")
	if !ok || ref.Match != matchTable || ref.Source != sourceProvimux || p.In != 9 || p.Src != sourceProvimux {
		t.Fatalf("a provider's table entry wins over an exact id and over *: %+v %+v", ref, p)
	}
	if ref, p, ok = book.resolvePrice("other", "vendor/x"); !ok || p.In != 7 || ref.ID != "provimux:*/vendor/x" {
		t.Fatalf("* applies to another provider: %+v %+v", ref, p)
	}
	if _, _, ok = book.resolvePrice("claude", "claude-opus-5-5"); ok {
		t.Fatal("same_as to an id the list lacks is unpriced, and no later step is tried")
	}
	table = tableOf(t, `{"provider":"*","model":"swe-2","same_as":"anthropic/claude-opus-5.5","source":"https://example.com/d"}`)
	if ref, p, ok = bookWith(list, table).resolvePrice("devin", "swe-2"); !ok || ref.ID != "anthropic/claude-opus-5.5" || ref.Match != matchTable || ref.Source != sourceOpenRouter || p.In != 4 {
		t.Fatalf("same_as takes the list's price: %+v %+v", ref, p)
	}
}

func TestResolveWhatIsNotRewritten(t *testing.T) {
	list := map[string]registry.ProvimuxOpenRouterPrice{
		"x-ai/grok-4.7-build":       {Input: 1, Output: 1},
		"anthropic/claude-x":        {Input: 1, Output: 1},
		"google/gemini-3-pro":       {Input: 1, Output: 1},
		"google/gemini-flash":       {Input: 1, Output: 1},
		"moonshotai/kimi-k2.7":      {Input: 1, Output: 1},
		"anthropic/claude-opus-4.6": {Input: 1, Output: 1},
		"openai/gpt-9":              {Input: 1, Output: 1},
		"vendor/negative":           {Input: -1, Output: 1},
	}
	book := bookWith(list, nil)
	for _, model := range []string{
		"grok-4.7-build-fast", "gemini-3-pro-preview", "gemini-flash-latest", "kimi-k2.7-code-highspeed",
		"claude-opus-4-6-1m", "claude-x-high-low-high", "swe-2", "acme-1", "vendor/negative",
	} {
		if ref, _, ok := book.resolvePrice("p", model); ok {
			t.Errorf("%s must be unpriced, got %+v", model, ref)
		}
	}
	if ref, _, ok := book.resolvePrice("p", "claude-x-high-low"); !ok || ref.ID != "anthropic/claude-x" {
		t.Errorf("two effort suffixes are removed: %+v", ref)
	}
	if ref, _, ok := book.resolvePrice("p", "gpt-9-thinking-xhigh"); !ok || ref.ID != "openai/gpt-9" {
		t.Errorf("-thinking and -xhigh are effort suffixes: %+v", ref)
	}
}

func TestDotted(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-4-5":          "claude-opus-4.5",
		"claude-3-7-sonnet":        "claude-3.7-sonnet",
		"gpt-5-6-sol":              "gpt-5.6-sol",
		"kimi-k3-1-pro":            "kimi-k3.1-pro",
		"grok-4.20-0309-reasoning": "grok-4.20-0309-reasoning",
	} {
		if got := dotted(in); got != want {
			t.Errorf("dotted(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithoutDate(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-4-5-20251101": "claude-opus-4-5",
		"gpt-4o-2024-08-06":        "gpt-4o",
		"grok-4.20-0309":           "grok-4.20-0309",
		"model":                    "model",
	} {
		if got := withoutDate(in); got != want {
			t.Errorf("withoutDate(%q) = %q, want %q", in, got, want)
		}
	}
}

// The repository forbids the standard regexp.
func TestNoRegexp(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, errParse := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if errParse != nil {
			t.Fatal(errParse)
		}
		for _, imp := range file.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == "regexp" || strings.HasSuffix(path, "/regexp") {
				t.Errorf("%s imports %s", entry.Name(), path)
			}
		}
	}
}
