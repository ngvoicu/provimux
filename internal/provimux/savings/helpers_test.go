package savings

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// designDir is the design this package is built from; its examples are the tests' expected values.
var designDir = filepath.Join("..", "..", "..", ".github", "provimux", "design", "savings")

func readDesign(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(designDir, name))
	if err != nil {
		t.Fatalf("read design file: %v", err)
	}
	return data
}

func fptr(v float64) *float64 { return &v }

// testList is OpenRouter's list of 2026-11-20 (testdata/openrouter-models.json), parsed by the
// rules of internal/registry/provimux_openrouter_prices.go.
func testList(t testing.TB) map[string]registry.ProvimuxOpenRouterPrice {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "openrouter-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct {
			ID      string         `json:"id"`
			Pricing map[string]any `json:"pricing"`
		} `json:"data"`
	}
	if errDecode := json.Unmarshal(data, &list); errDecode != nil {
		t.Fatal(errDecode)
	}
	perMillion := func(v any) (float64, bool) {
		s, ok := v.(string)
		if !ok {
			return 0, false
		}
		f, errParse := strconv.ParseFloat(s, 64)
		if errParse != nil || f < 0 {
			return 0, false
		}
		return math.Round(f*1e6*1e9) / 1e9, true
	}
	out := make(map[string]registry.ProvimuxOpenRouterPrice)
	for _, m := range list.Data {
		in, okIn := perMillion(m.Pricing["prompt"])
		o, okOut := perMillion(m.Pricing["completion"])
		if !okIn || !okOut {
			continue
		}
		p := registry.ProvimuxOpenRouterPrice{Input: in, Output: o}
		if v, ok := perMillion(m.Pricing["input_cache_read"]); ok {
			p.CacheRead = fptr(v)
		}
		if v, ok := perMillion(m.Pricing["input_cache_write"]); ok {
			p.CacheWrite = fptr(v)
		}
		if v, ok := perMillion(m.Pricing["input_cache_write_1h"]); ok {
			p.CacheWrite1h = fptr(v)
		}
		out[strings.ToLower(m.ID)] = p
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
