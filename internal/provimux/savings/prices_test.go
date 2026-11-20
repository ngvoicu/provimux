package savings

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

// validateTable applies the rules of schemas/prices-table.schema.json to a table file.
func validateTable(data []byte) []string {
	var errs []string
	fail := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return []string{"not JSON: " + err.Error()}
	}
	for key := range root {
		if !slices.Contains([]string{"v", "updated", "models"}, key) {
			fail("unknown property %q", key)
		}
	}
	if v, ok := root["v"].(float64); !ok || v != 1 {
		fail("v must be 1")
	}
	if updated, ok := root["updated"].(string); !ok || !isDay(updated) {
		fail("updated must be a day")
	}
	models, ok := root["models"].([]any)
	if !ok {
		fail("models must be an array")
		return errs
	}
	seen := map[string]bool{}
	for i, raw := range models {
		entry, isObject := raw.(map[string]any)
		if !isObject {
			fail("models[%d] is not an object", i)
			continue
		}
		for key := range entry {
			if !slices.Contains([]string{"provider", "model", "price", "same_as", "source", "note"}, key) {
				fail("models[%d]: unknown property %q", i, key)
			}
		}
		provider, _ := entry["provider"].(string)
		model, _ := entry["model"].(string)
		if provider == "" || model == "" {
			fail("models[%d]: provider and model are required", i)
		}
		if provider != strings.ToLower(provider) || model != strings.ToLower(model) {
			fail("models[%d]: provider and model must be lower case", i)
		}
		if seen[provider+"\x00"+model] {
			fail("models[%d]: %s %s appears twice", i, provider, model)
		}
		seen[provider+"\x00"+model] = true
		source, _ := entry["source"].(string)
		if parsed, err := url.Parse(source); err != nil || !strings.HasPrefix(source, "https://") || parsed.Host == "" {
			fail("models[%d]: source must be an https URL", i)
		}
		_, hasPrice := entry["price"]
		_, hasSameAs := entry["same_as"]
		if hasPrice == hasSameAs {
			fail("models[%d]: exactly one of price and same_as", i)
		}
		if hasSameAs {
			sameAs, _ := entry["same_as"].(string)
			if !validSameAs(sameAs) {
				fail("models[%d]: same_as %q is not an OpenRouter id", i, sameAs)
			}
		}
		if hasPrice {
			p, isPrice := entry["price"].(map[string]any)
			if !isPrice {
				fail("models[%d]: price is not an object", i)
				continue
			}
			for key, value := range p {
				if !slices.Contains([]string{"in", "out", "cr", "cw", "cw1h"}, key) {
					fail("models[%d]: price has unknown %q", i, key)
				}
				if f, isNumber := value.(float64); !isNumber || f < 0 {
					fail("models[%d]: price.%s must be a number >= 0", i, key)
				}
			}
			if _, has := p["in"]; !has {
				fail("models[%d]: price.in is required", i)
			}
			if _, has := p["out"]; !has {
				fail("models[%d]: price.out is required", i)
			}
		}
	}
	return errs
}

func isDay(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func validSameAs(s string) bool {
	slash := strings.IndexByte(s, '/')
	if slash <= 0 || slash == len(s)-1 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' || (i > slash && c == ':') || i == slash
		if !ok {
			return false
		}
	}
	return true
}

func TestTheTableFile(t *testing.T) {
	if errs := validateTable(embeddedPriceTable); len(errs) != 0 {
		t.Fatalf("prices.json: %v", errs)
	}
	if _, err := parseTable(embeddedPriceTable); err != nil {
		t.Fatal(err)
	}
	if !isDay(defaultTable.Updated) || defaultTable.V != 1 {
		t.Fatalf("embedded table: %+v", defaultTable)
	}
	if errs := validateTable(readDesign(t, "examples/prices-table.json")); len(errs) != 0 {
		t.Fatalf("examples/prices-table.json: %v", errs)
	}
	planted := `{"v":1,"updated":"2026-13-01","extra":1,"models":[
		{"provider":"Devin","model":"a","price":{"in":1},"source":"http://x"},
		{"provider":"devin","model":"b","price":{"in":1,"out":1},"same_as":"x/y","source":"https://x"},
		{"provider":"devin","model":"b","same_as":"Not An Id","source":"https://x"}]}`
	errs := validateTable([]byte(planted))
	for _, want := range []string{"unknown property \"extra\"", "updated", "lower case", "https", "price.out", "exactly one", "twice", "not an OpenRouter id"} {
		found := false
		for _, e := range errs {
			found = found || strings.Contains(e, want)
		}
		if !found {
			t.Errorf("planted fault %q not found in %v", want, errs)
		}
	}
}

func TestTableEntriesAreUsed(t *testing.T) {
	table := tableOf(t, `{"provider":"devin","model":"swe-2","price":{"in":1.5,"out":6,"cr":0.15},"source":"https://example.com/p"}`)
	ref, p, ok := bookWith(nil, table).resolvePrice("devin", "swe-2")
	if !ok || ref.ID != "provimux:devin/swe-2" || p.Src != sourceProvimux || p.Cr == nil || *p.Cr != 0.15 || p.Cw != nil {
		t.Fatalf("got %+v %+v %v", ref, p, ok)
	}
	if _, _, ok = bookWith(nil, table).resolvePrice("claude", "swe-2"); ok {
		t.Fatal("a provider's entry applies to that provider only")
	}
}
